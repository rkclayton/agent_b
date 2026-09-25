package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"harness/internal/credential"
	"harness/internal/events"
)

type Subscription struct {
	Endpoint string                        `json:"endpoint"`
	Keys     struct{ P256DH, Auth string } `json:"keys"`
}
type State struct {
	Enabled       bool   `json:"enabled"`
	PublicKey     string `json:"public_key,omitempty"`
	Subscriptions int    `json:"subscriptions"`
}
type persisted struct {
	Enabled       bool           `json:"enabled"`
	Subscriptions []Subscription `json:"subscriptions"`
}
type doer interface {
	Do(*http.Request) (*http.Response, error)
}
type Manager struct {
	mu      sync.Mutex
	root    string
	key     *credential.Store
	data    persisted
	bus     *events.Bus
	label   func(string) string
	client  doer
	now     func() time.Time
	wait    func(context.Context, time.Duration) error
	started bool
}

func New(root string, bus *events.Bus, label func(string) string) *Manager {
	m := &Manager{bus: bus, label: label, client: &http.Client{Timeout: 10 * time.Second}, now: time.Now}
	m.wait = func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.SetRoot(root)
	return m
}
func (m *Manager) SetRoot(root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.root = root
	m.data = persisted{}
	m.key, _ = credential.NewNamed(root, "web-push-vapid")
	if raw, err := os.ReadFile(filepath.Join(root, "phone-push.json")); err == nil {
		_ = json.Unmarshal(raw, &m.data)
	}
	if m.data.Enabled {
		m.startLocked()
	}
}
func (m *Manager) startLocked() {
	if m.started || m.bus == nil {
		return
	}
	m.started = true
	stream, _ := m.bus.Subscribe()
	go func() {
		for event := range stream {
			if wanted(event.Type) {
				go func() { _ = m.Deliver(context.Background(), event) }()
			}
		}
	}()
}
func wanted(kind string) bool {
	return kind == events.ApprovalRequired || kind == events.RunStopped || kind == events.ItemStuck
}
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := State{Enabled: m.data.Enabled, Subscriptions: len(m.data.Subscriptions)}
	if m.data.Enabled {
		if key, err := m.privateLocked(); err == nil {
			state.PublicKey = base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), key.PublicKey.X, key.PublicKey.Y))
		}
	}
	return state
}
func (m *Manager) Enable(value bool) (State, error) {
	m.mu.Lock()
	if value {
		if _, err := m.privateLocked(); err != nil {
			m.mu.Unlock()
			return State{}, err
		}
		m.startLocked()
	}
	m.data.Enabled = value
	err := m.saveLocked()
	m.mu.Unlock()
	return m.State(), err
}
func (m *Manager) Add(subscription Subscription) error {
	if err := validate(subscription); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.data.Subscriptions {
		if m.data.Subscriptions[i].Endpoint == subscription.Endpoint {
			m.data.Subscriptions[i] = subscription
			return m.saveLocked()
		}
	}
	m.data.Subscriptions = append(m.data.Subscriptions, subscription)
	return m.saveLocked()
}
func (m *Manager) Remove(endpoint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(endpoint)
	return m.saveLocked()
}
func (m *Manager) removeLocked(endpoint string) {
	kept := m.data.Subscriptions[:0]
	for _, s := range m.data.Subscriptions {
		if s.Endpoint != endpoint {
			kept = append(kept, s)
		}
	}
	m.data.Subscriptions = kept
}
func validate(s Subscription) error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid push endpoint")
	}
	local := u.Scheme == "http" && (strings.EqualFold(u.Hostname(), "localhost") || net.ParseIP(u.Hostname()).IsLoopback())
	if u.Scheme != "https" && !local {
		return fmt.Errorf("push endpoint must use HTTPS")
	}
	if _, err = base64.RawURLEncoding.DecodeString(s.Keys.P256DH); err != nil {
		return fmt.Errorf("invalid p256dh key")
	}
	if _, err = base64.RawURLEncoding.DecodeString(s.Keys.Auth); err != nil {
		return fmt.Errorf("invalid auth key")
	}
	return nil
}
func (m *Manager) saveLocked() error {
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.root, "phone-push.json"), append(raw, '\n'), 0600)
}
func (m *Manager) privateLocked() (*ecdsa.PrivateKey, error) {
	if m.key == nil {
		return nil, fmt.Errorf("push credential store unavailable")
	}
	raw, err := m.key.Read()
	if err == credential.ErrNotStored {
		key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			return nil, e
		}
		raw = key.D.FillBytes(make([]byte, 32))
		if e = m.key.Write(raw); e != nil {
			return nil, e
		}
	} else if err != nil {
		return nil, err
	}
	d := new(big.Int).SetBytes(raw)
	x, y := elliptic.P256().ScalarBaseMult(raw)
	if d.Sign() == 0 || x == nil {
		return nil, fmt.Errorf("invalid VAPID key")
	}
	return &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, D: d}, nil
}
func (m *Manager) Deliver(ctx context.Context, event events.Event) error {
	if !wanted(event.Type) {
		return nil
	}
	m.mu.Lock()
	if !m.data.Enabled {
		m.mu.Unlock()
		return nil
	}
	subs := append([]Subscription(nil), m.data.Subscriptions...)
	key, err := m.privateLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(m.payload(event))
	var last error
	for _, sub := range subs {
		for attempt, delay := range []time.Duration{0, 30 * time.Second, 60 * time.Second, 120 * time.Second} {
			if delay > 0 {
				if err = m.wait(ctx, delay); err != nil {
					return err
				}
			}
			status, sendErr := m.send(ctx, key, sub, payload)
			last = sendErr
			if status >= 200 && status < 300 {
				break
			}
			if status == 404 || status == 410 {
				_ = m.Remove(sub.Endpoint)
				break
			}
			if attempt == 3 {
				log.Printf("web push dropped after retries: endpoint=%s status=%d error=%v", urlHost(sub.Endpoint), status, sendErr)
			}
		}
	}
	return last
}
func (m *Manager) payload(event events.Event) map[string]string {
	data, _ := event.Data.(map[string]any)
	notice := events.HumanNoticeFor(event.Type, data)
	label := "chat"
	if m.label != nil {
		if value := strings.TrimSpace(m.label(event.SessionID)); value != "" {
			label = value
		}
	}
	return map[string]string{"title": "Agent_b · " + label, "body": notice.Happened, "chat_id": event.SessionID, "url": "/phone?chat=" + url.QueryEscape(event.SessionID)}
}
func (m *Manager) send(ctx context.Context, key *ecdsa.PrivateKey, sub Subscription, payload []byte) (int, error) {
	body, err := encrypt(sub, payload)
	if err != nil {
		return 0, err
	}
	token, public, err := vapid(key, sub.Endpoint, m.now())
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "300")
	req.Header.Set("Authorization", "vapid t="+token+", k="+public)
	response, err := m.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("push service returned HTTP %d", response.StatusCode)
	}
	return response.StatusCode, nil
}
func encrypt(sub Subscription, plain []byte) ([]byte, error) {
	clientRaw, e := base64.RawURLEncoding.DecodeString(sub.Keys.P256DH)
	if e != nil {
		return nil, e
	}
	auth, e := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	if e != nil {
		return nil, e
	}
	curve := ecdh.P256()
	client, e := curve.NewPublicKey(clientRaw)
	if e != nil {
		return nil, e
	}
	server, e := curve.GenerateKey(rand.Reader)
	if e != nil {
		return nil, e
	}
	secret, e := server.ECDH(client)
	if e != nil {
		return nil, e
	}
	info := append(append([]byte("WebPush: info\x00"), clientRaw...), server.PublicKey().Bytes()...)
	ikm := expand(extract(auth, secret), info, 32)
	salt := make([]byte, 16)
	if _, e = rand.Read(salt); e != nil {
		return nil, e
	}
	prk := extract(salt, ikm)
	cek := expand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := expand(prk, []byte("Content-Encoding: nonce\x00"), 12)
	block, e := aes.NewCipher(cek)
	if e != nil {
		return nil, e
	}
	gcm, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	sealed := gcm.Seal(nil, nonce, append(plain, 2), nil)
	out := append([]byte{}, salt...)
	size := make([]byte, 4)
	binary.BigEndian.PutUint32(size, 4096)
	out = append(out, size...)
	pub := server.PublicKey().Bytes()
	out = append(out, byte(len(pub)))
	out = append(out, pub...)
	return append(out, sealed...), nil
}
func extract(salt, value []byte) []byte {
	h := hmac.New(sha256.New, salt)
	_, _ = h.Write(value)
	return h.Sum(nil)
}
func expand(prk, info []byte, n int) []byte {
	out, prior := []byte{}, []byte{}
	for counter := byte(1); len(out) < n; counter++ {
		h := hmac.New(sha256.New, prk)
		_, _ = h.Write(prior)
		_, _ = h.Write(info)
		_, _ = h.Write([]byte{counter})
		prior = h.Sum(nil)
		out = append(out, prior...)
	}
	return out[:n]
}
func vapid(key *ecdsa.PrivateKey, endpoint string, now time.Time) (string, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", "", err
	}
	aud := u.Scheme + "://" + u.Host
	enc := func(v any) string { raw, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(raw) }
	unsigned := enc(map[string]string{"typ": "JWT", "alg": "ES256"}) + "." + enc(map[string]any{"aud": aud, "exp": now.Add(12 * time.Hour).Unix(), "sub": "mailto:operator@agentb.local"})
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", "", err
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	pub := elliptic.Marshal(elliptic.P256(), key.PublicKey.X, key.PublicKey.Y)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig), base64.RawURLEncoding.EncodeToString(pub), nil
}
func urlHost(raw string) string { u, _ := url.Parse(raw); return u.Host }
