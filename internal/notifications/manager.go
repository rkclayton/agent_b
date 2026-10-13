package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"harness/internal/events"
)

const retryDelay = 30 * time.Second

type doer interface {
	Do(*http.Request) (*http.Response, error)
}

type State struct {
	Configured bool   `json:"configured"`
	Host       string `json:"host,omitempty"`
	Desktop    bool   `json:"desktop"`
	Phone      string `json:"phone"`
}

type DesktopNotice struct{ Title, Body, ChatID, Link string }
type Desktop interface {
	Viewing(string) bool
	Select(string)
	Show(DesktopNotice) error
}
type Phone interface {
	NotificationState() string
	Notify(context.Context, string, string, string) (string, error)
}

type Manager struct {
	bus            *events.Bus
	client         doer
	label          func(string) string
	baseURL        string
	allowLocal     bool
	wait           func(context.Context, time.Duration) error
	desktop        Desktop
	phone          Phone
	desktopEnabled func() bool

	mu             sync.RWMutex
	webhook        *url.URL
	webhookContext context.Context
	webhookCancel  context.CancelFunc
	cancel         context.CancelFunc
	unsub          func()
	started        bool
}

func (m *Manager) SetChannels(desktop Desktop, phone Phone, enabled func() bool) {
	m.mu.Lock()
	m.desktop, m.phone, m.desktopEnabled = desktop, phone, enabled
	m.mu.Unlock()
}
func (m *Manager) SetViewing(session string) {
	m.mu.RLock()
	desktop := m.desktop
	m.mu.RUnlock()
	if desktop != nil {
		desktop.Select(session)
	}
}
func New(bus *events.Bus, label func(string) string, baseURL string) *Manager {
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("redirects are refused")
		},
	}
	return &Manager{
		bus: bus, client: client, label: label, baseURL: strings.TrimRight(baseURL, "/"),
		wait: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
}

func (m *Manager) Configure(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		m.mu.Lock()
		if m.webhookCancel != nil {
			m.webhookCancel()
		}
		m.webhook = nil
		m.webhookContext, m.webhookCancel = nil, nil
		m.mu.Unlock()
		return nil
	}
	endpoint, err := parseWebhook(value, m.allowLocal)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.webhookCancel != nil {
		m.webhookCancel()
	}
	m.webhook = endpoint
	m.webhookContext, m.webhookCancel = context.WithCancel(context.Background())
	m.mu.Unlock()
	return nil
}

// ConfigureLoopbackFixture is the executable acceptance seam. Normal
// configuration still accepts only Discord HTTPS webhook URLs; this method is
// intentionally limited to an HTTP loopback receiver supplied by the harness.
func (m *Manager) ConfigureLoopbackFixture(raw string) error {
	endpoint, err := parseWebhook(strings.TrimSpace(raw), true)
	if err != nil || endpoint.Scheme != "http" || !net.ParseIP(endpoint.Hostname()).IsLoopback() {
		return errors.New("notification fixture must be one HTTP loopback URL")
	}
	m.mu.Lock()
	if m.webhookCancel != nil {
		m.webhookCancel()
	}
	m.webhook = endpoint
	m.webhookContext, m.webhookCancel = context.WithCancel(context.Background())
	m.mu.Unlock()
	return nil
}

func (m *Manager) Validate(raw string) error {
	_, err := parseWebhook(strings.TrimSpace(raw), m.allowLocal)
	return err
}

func (m *Manager) State() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	enabled := false
	if m.desktopEnabled != nil {
		enabled = m.desktopEnabled()
	}
	state := State{Desktop: enabled, Phone: "not paired"}
	if m.phone != nil {
		state.Phone = m.phone.NotificationState()
	}
	if m.webhook == nil {
		return state
	}
	state.Configured, state.Host = true, m.webhook.Hostname()
	return state
}

func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	workerContext, cancel := context.WithCancel(ctx)
	stream := m.bus.Listen(workerContext, "notifications")
	m.started, m.unsub, m.cancel = true, nil, cancel
	m.mu.Unlock()
	jobs := make(chan events.Event, 128)
	go func() {
		defer close(jobs)
		for {
			select {
			case <-workerContext.Done():
				return
			case event, ok := <-stream:
				if !ok {
					return
				}
				data, _ := event.Data.(map[string]any)
				if data["notification_suppressed"] == true || !notifiable(event.Type) {
					continue
				}
				select {
				case jobs <- event:
				case <-workerContext.Done():
					return
				}
			}
		}
	}()
	go func() {
		for event := range jobs {
			m.deliverEvent(workerContext, event)
		}
	}()
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	if m.unsub != nil {
		m.unsub()
	}
	m.cancel, m.unsub, m.started = nil, nil, false
	m.mu.Unlock()
}

func (m *Manager) SendTest(ctx context.Context) (map[string]string, error) {
	m.mu.RLock()
	desktop, phone, enabled, webhook := m.desktop, m.phone, m.desktopEnabled, m.webhook != nil
	m.mu.RUnlock()
	desktopOn := enabled != nil && enabled()
	answers := map[string]string{}
	if desktopOn && desktop != nil {
		if err := desktop.Show(DesktopNotice{Title: "Agent_b", Body: "Test notification", Link: m.baseURL}); err != nil {
			answers["desktop"] = "refused"
		} else {
			answers["desktop"] = "shown"
		}
	}
	if webhook {
		if err := m.deliver(ctx, "test", "Agent_b test notification.\nThe harness sent this from Settings without starting a run.\n"+m.baseURL); err != nil {
			answers["webhook"] = "refused"
		} else {
			answers["webhook"] = "accepted"
		}
	}
	if phone != nil {
		answer, err := phone.Notify(ctx, "run_stopped", "", "Agent_b test notification")
		if err != nil {
			answer = "refused"
		}
		if answer == "" {
			answer = phone.NotificationState()
		}
		answers["phone"] = answer
	}
	return answers, nil
}

func (m *Manager) deliverEvent(ctx context.Context, event events.Event) {
	m.mu.RLock()
	desktop, phone, enabled, webhook := m.desktop, m.phone, m.desktopEnabled, m.webhook != nil
	m.mu.RUnlock()
	desktopOn := enabled != nil && enabled()
	data, _ := event.Data.(map[string]any)
	notice := humanNotice(data, event.Type)
	title := "chat"
	if job, ok := data["scheduled_job"].(string); ok && strings.TrimSpace(job) != "" {
		title = strings.TrimSpace(job)
	} else if m.label != nil {
		if value := strings.TrimSpace(m.label(event.SessionID)); value != "" {
			title = value
		}
	}
	body := notice.Happened
	if notice.HarnessAction != "" {
		body += "\n" + notice.HarnessAction
	}
	if lines := strings.Split(body, "\n"); len(lines) > 2 {
		body = strings.Join(lines[:2], "\n")
	}
	link := m.baseURL + "/chat?session=" + url.QueryEscape(event.SessionID)
	if data["role"] == "c" {
		link = m.baseURL + "/plan"
	}
	if desktopOn && desktop != nil && !desktop.Viewing(event.SessionID) {
		_ = desktop.Show(DesktopNotice{Title: title, Body: body, ChatID: event.SessionID, Link: link})
	}
	if webhook {
		_ = m.deliver(ctx, event.Type, m.message(event))
	}
	if phone != nil {
		_, _ = phone.Notify(ctx, pushKind(event.Type), event.SessionID, strings.ReplaceAll(body, "\n", " "))
	}
}

func pushKind(eventType string) string {
	if eventType == events.ApprovalRequired {
		return "approval_required"
	}
	if eventType == events.ItemStuck {
		return "item_stuck"
	}
	return "run_stopped"
}

func (m *Manager) deliver(ctx context.Context, eventType, message string) error {
	m.mu.RLock()
	endpoint, configuredContext := m.webhook, m.webhookContext
	m.mu.RUnlock()
	if endpoint == nil || configuredContext == nil {
		return errors.New("webhook is not configured")
	}
	deliveryContext, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(configuredContext, cancel)
	defer func() { stopCancel(); cancel() }()
	err := m.post(deliveryContext, endpoint, message)
	if err == nil {
		return nil
	}
	if waitErr := m.wait(deliveryContext, retryDelay); waitErr != nil {
		return waitErr
	}
	err = m.post(deliveryContext, endpoint, message)
	if err == nil {
		return nil
	}
	safe := safeFailure(err)
	m.bus.Publish(events.New(events.NotificationFailed, "", "", map[string]any{
		"sink": "discord", "event_type": eventType, "attempts": 2,
		"message": "Discord notification failed after one retry: " + safe + "; check Settings > Notifications and the webhook destination",
	}))
	return fmt.Errorf("Discord notification failed after one retry: %s", safe)
}

func (m *Manager) post(ctx context.Context, endpoint *url.URL, message string) error {
	payload := struct {
		Content         string `json:"content"`
		AllowedMentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}{Content: message}
	payload.AllowedMentions.Parse = []string{}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return errors.New("could not prepare the webhook request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := m.client.Do(request)
	if err != nil {
		return errors.New("the webhook host could not be reached")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("the webhook returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (m *Manager) message(event events.Event) string {
	data, _ := event.Data.(map[string]any)
	notice := humanNotice(data, event.Type)
	label := "chat"
	if m.label != nil {
		if value := strings.TrimSpace(m.label(event.SessionID)); value != "" {
			label = value
		}
	}
	parts := []string{"Agent_b · " + label, notice.Happened}
	if notice.HarnessAction != "" {
		parts = append(parts, notice.HarnessAction)
	}
	if notice.Question != "" {
		parts = append(parts, notice.Question)
	}
	if len(notice.Actions) > 0 {
		parts = append(parts, "Actions: "+strings.Join(notice.Actions, " / "))
	}
	link := m.baseURL
	if data["role"] == "c" && data["plan_id"] != nil && data["plan_id"] != "" {
		// A worker has no chat; what it waits on is drawn on the Plan page.
		link += "/plan"
	} else if event.SessionID != "" {
		link += "/chat?session=" + url.QueryEscape(event.SessionID)
	}
	parts = append(parts, link)
	return strings.Join(parts, "\n")
}

func humanNotice(data map[string]any, eventType string) events.HumanNotice {
	if raw, ok := data["human"]; ok {
		encoded, _ := json.Marshal(raw)
		var notice events.HumanNotice
		if json.Unmarshal(encoded, &notice) == nil && notice.Happened != "" {
			return notice
		}
	}
	return events.HumanNoticeFor(eventType, data)
}

func notifiable(eventType string) bool {
	switch eventType {
	case events.ApprovalRequired, events.RunStopped, events.ItemDone, events.ItemStuck, events.PlanDone, events.WorkerJob:
		return true
	default:
		return false
	}
}

func parseWebhook(raw string, allowLocal bool) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("Discord URL must be one HTTPS webhook URL without credentials, query, or fragment")
	}
	host := strings.ToLower(endpoint.Hostname())
	if allowLocal && endpoint.Scheme == "http" && (host == "localhost" || net.ParseIP(host).IsLoopback()) {
		return endpoint, nil
	}
	if endpoint.Scheme != "https" || !(host == "discord.com" || host == "canary.discord.com" || host == "ptb.discord.com") || !strings.HasPrefix(endpoint.EscapedPath(), "/api/webhooks/") {
		return nil, errors.New("Discord URL must be an https://discord.com/api/webhooks/... endpoint")
	}
	return endpoint, nil
}

func safeFailure(err error) string {
	if err == nil {
		return "unknown failure"
	}
	return strings.TrimSpace(err.Error())
}
