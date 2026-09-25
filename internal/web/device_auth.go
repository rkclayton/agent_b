package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	pushnotify "harness/internal/push"
)

const (
	phoneRedeemPath = "/api/phone/enrolment/redeem"
	phoneCodeTTL    = 5 * time.Minute
	phoneRateWindow = time.Minute
	phoneRateLimit  = 5
)

type phoneDeviceView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	EnrolledAt string `json:"enrolled_at"`
	LastSeen   string `json:"last_seen"`
}

type phoneDevice struct {
	phoneDeviceView
	tokenHash [sha256.Size]byte
	revoked   chan struct{}
}

type phoneAttempt struct {
	start time.Time
	count int
}

type phoneDevices struct {
	mu          sync.Mutex
	now         func() time.Time
	offerHash   [sha256.Size]byte
	offerExpiry time.Time
	devices     map[string]*phoneDevice
	attempts    map[string]phoneAttempt
}

func newPhoneDevices() *phoneDevices {
	return &phoneDevices{now: time.Now, devices: map[string]*phoneDevice{}, attempts: map[string]phoneAttempt{}}
}

func randomURLToken(bytes int) string {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		panic("generate phone credential: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func (p *phoneDevices) offer() (string, time.Time) {
	value, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic("generate phone enrolment code: " + err.Error())
	}
	code := fmt.Sprintf("%06d", value.Int64())
	p.mu.Lock()
	p.offerHash = sha256.Sum256([]byte(code))
	p.offerExpiry = p.now().Add(phoneCodeTTL)
	expires := p.offerExpiry
	p.mu.Unlock()
	return code, expires
}

func (p *phoneDevices) redeem(code, name, remote string) (string, phoneDeviceView, int) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return "", phoneDeviceView{}, http.StatusBadRequest
	}
	now := p.now()
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	attempt := p.attempts[host]
	if attempt.start.IsZero() || now.Sub(attempt.start) >= phoneRateWindow {
		attempt = phoneAttempt{start: now}
	}
	if attempt.count >= phoneRateLimit {
		return "", phoneDeviceView{}, http.StatusTooManyRequests
	}
	wanted := sha256.Sum256([]byte(code))
	valid := !p.offerExpiry.IsZero() && now.Before(p.offerExpiry) && subtle.ConstantTimeCompare(wanted[:], p.offerHash[:]) == 1
	if !valid {
		attempt.count++
		p.attempts[host] = attempt
		return "", phoneDeviceView{}, http.StatusUnauthorized
	}
	p.offerHash = [sha256.Size]byte{}
	p.offerExpiry = time.Time{}
	delete(p.attempts, host)
	token := randomURLToken(32)
	id := randomURLToken(12)
	view := phoneDeviceView{ID: id, Name: name, EnrolledAt: now.UTC().Format(time.RFC3339), LastSeen: now.UTC().Format(time.RFC3339)}
	p.devices[id] = &phoneDevice{phoneDeviceView: view, tokenHash: sha256.Sum256([]byte(token)), revoked: make(chan struct{})}
	return token, view, http.StatusOK
}

func (p *phoneDevices) authenticate(token string) (*phoneDevice, bool) {
	if token == "" {
		return nil, false
	}
	wanted := sha256.Sum256([]byte(token))
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, device := range p.devices {
		if subtle.ConstantTimeCompare(wanted[:], device.tokenHash[:]) == 1 {
			device.LastSeen = p.now().UTC().Format(time.RFC3339)
			return device, true
		}
	}
	return nil, false
}

func (p *phoneDevices) list() []phoneDeviceView {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]phoneDeviceView, 0, len(p.devices))
	for _, device := range p.devices {
		result = append(result, device.phoneDeviceView)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].EnrolledAt < result[j].EnrolledAt })
	return result
}

func (p *phoneDevices) revoke(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	device, ok := p.devices[id]
	if !ok {
		return false
	}
	delete(p.devices, id)
	close(device.revoked)
	return true
}

func (p *phoneDevices) revokeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, device := range p.devices {
		delete(p.devices, id)
		close(device.revoked)
	}
}

type phoneDeviceContextKey struct{}

func phoneBearer(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) < 8 || !strings.EqualFold(value[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(value[7:])
}

func phoneAuthenticated(r *http.Request) bool {
	_, ok := r.Context().Value(phoneDeviceContextKey{}).(string)
	return ok
}

func (s *Server) phoneSessionGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		device, ok := s.phoneDevices.authenticate(phoneBearer(r))
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		ctx = context.WithValue(ctx, phoneDeviceContextKey{}, device.ID)
		done := make(chan struct{})
		go func() {
			select {
			case <-device.revoked:
				cancel()
			case <-done:
			}
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
		close(done)
	})
}

func (s *Server) phoneEnrolment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	code, expires := s.phoneDevices.offer()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"code": code, "expires_at": expires.UTC().Format(time.RFC3339)})
}

func (s *Server) phoneAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		method(w)
		return
	}
	name, cache := "phone.html", "no-store"
	if r.URL.Path == "/phone-sw.js" {
		name, cache = "phone-sw.js", "no-cache"
		w.Header().Set("Service-Worker-Allowed", "/")
	}
	w.Header().Set("Cache-Control", cache)
	http.ServeFile(w, r, filepath.Join(s.webDir, name))
}

func (s *Server) phoneEnrolmentRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var input struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&input); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	token, device, status := s.phoneDevices.redeem(strings.TrimSpace(input.Code), input.Name, r.RemoteAddr)
	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"credential": token, "device": device})
}

func (s *Server) phoneDeviceList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	state := s.push.State()
	writeJSON(w, http.StatusOK, map[string]any{"devices": s.phoneDevices.list(), "push_enabled": state.Enabled, "push_subscriptions": state.Subscriptions})
}

func (s *Server) phoneDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if strings.HasSuffix(r.URL.Path, "revoke-all") {
		s.phoneDevices.revokeAll()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var input struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&input); err != nil || strings.TrimSpace(input.ID) == "" {
		writeError(w, http.StatusBadRequest, "device id is required", "id")
		return
	}
	if !s.phoneDevices.revoke(strings.TrimSpace(input.ID)) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) phonePush(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.push.State())
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &input) {
		return
	}
	state, err := s.push.Enable(input.Enabled)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "enabled")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) phonePushSubscription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var input struct {
		Action string `json:"action"`
		pushnotify.Subscription
	}
	if !decode(w, r, &input) {
		return
	}
	var err error
	if input.Action == "remove" {
		err = s.push.Remove(input.Endpoint)
	} else {
		err = s.push.Add(input.Subscription)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "subscription")
		return
	}
	writeJSON(w, http.StatusOK, s.push.State())
}
