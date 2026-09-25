package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
)

func phoneTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults(root)
	s := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root, Workspace: cfg.Workspace}, events.NewBus())
	return s, s.Handler()
}

func phoneCall(s *Server, h http.Handler, method, path, body, auth string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth == "desktop" {
		authorizeMutation(r, s)
		r.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: s.browserSession})
	} else if auth != "" {
		r.Header.Set("Authorization", "Bearer "+auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPhoneEnrollmentMintsAndRevokesOneDevice(t *testing.T) {
	server, handler := phoneTestServer(t)
	generated := phoneCall(server, handler, http.MethodPost, "/api/phone/enrolment", "", "desktop")
	if generated.Code != http.StatusOK {
		t.Fatalf("generate status=%d body=%s", generated.Code, generated.Body)
	}
	var offer struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(generated.Body.Bytes(), &offer); err != nil || len(offer.Code) != 6 {
		t.Fatalf("offer=%+v err=%v", offer, err)
	}
	stateBeforeRedeemResponse := phoneCall(server, handler, http.MethodGet, "/api/state", "", "desktop")
	if strings.Contains(stateBeforeRedeemResponse.Body.String(), offer.Code) {
		t.Fatal("enrolment code leaked into /api/state")
	}

	redeemed := phoneCall(server, handler, http.MethodPost, "/api/phone/enrolment/redeem", `{"code":"`+offer.Code+`","name":"Randy's phone"}`, "")
	if redeemed.Code != http.StatusOK {
		t.Fatalf("redeem status=%d body=%s", redeemed.Code, redeemed.Body)
	}
	var session struct {
		Credential string `json:"credential"`
		Device     struct {
			ID string `json:"id"`
		} `json:"device"`
	}
	if err := json.Unmarshal(redeemed.Body.Bytes(), &session); err != nil || session.Credential == "" || session.Device.ID == "" {
		t.Fatalf("session=%+v err=%v", session, err)
	}

	allowed := phoneCall(server, handler, http.MethodGet, "/api/state", "", session.Credential)
	if allowed.Code != http.StatusOK {
		t.Fatalf("device state status=%d body=%s", allowed.Code, allowed.Body)
	}
	deviceMutationResponse := phoneCall(server, handler, http.MethodPost, "/api/config", `{"approval":{"mode":"all"}}`, session.Credential)
	if deviceMutationResponse.Code != http.StatusOK {
		t.Fatalf("device full-authority mutation status=%d body=%s", deviceMutationResponse.Code, deviceMutationResponse.Body)
	}

	reused := phoneCall(server, handler, http.MethodPost, "/api/phone/enrolment/redeem", `{"code":"`+offer.Code+`","name":"second"}`, "")
	if reused.Code != http.StatusUnauthorized || reused.Body.Len() != 0 {
		t.Fatalf("reused code status=%d body=%s", reused.Code, reused.Body)
	}

	revoked := phoneCall(server, handler, http.MethodPost, "/api/phone/devices/revoke", `{"id":"`+session.Device.ID+`"}`, "desktop")
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", revoked.Code, revoked.Body)
	}

	denied := phoneCall(server, handler, http.MethodGet, "/api/state", "", session.Credential)
	if denied.Code != http.StatusUnauthorized || denied.Body.Len() != 0 {
		t.Fatalf("revoked state status=%d body=%s", denied.Code, denied.Body)
	}

	desktopResponse := phoneCall(server, handler, http.MethodGet, "/api/state", "", "desktop")
	if desktopResponse.Code != http.StatusOK {
		t.Fatalf("desktop state after revoke status=%d body=%s", desktopResponse.Code, desktopResponse.Body)
	}
}

func TestPhoneEnrollmentExpiryRateLimitAndRevocationCancel(t *testing.T) {
	server, _ := phoneTestServer(t)
	now := time.Date(2026, 9, 25, 15, 0, 0, 0, time.UTC)
	server.phoneDevices.now = func() time.Time { return now }

	expired, _ := server.phoneDevices.offer()
	now = now.Add(phoneCodeTTL)
	if _, _, status := server.phoneDevices.redeem(expired, "expired", "192.0.2.1:1000"); status != http.StatusUnauthorized {
		t.Fatalf("expired code status=%d", status)
	}
	for attempt := 1; attempt <= phoneRateLimit; attempt++ {
		if _, _, status := server.phoneDevices.redeem("wrong", "limited", "192.0.2.2:1000"); status != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d", attempt, status)
		}
	}
	if _, _, status := server.phoneDevices.redeem("wrong", "limited", "192.0.2.2:1000"); status != http.StatusTooManyRequests {
		t.Fatalf("rate-limited status=%d", status)
	}
	now = now.Add(phoneRateWindow)
	if _, _, status := server.phoneDevices.redeem("wrong", "limited", "192.0.2.2:1000"); status != http.StatusUnauthorized {
		t.Fatalf("reset rate status=%d", status)
	}

	code, _ := server.phoneDevices.offer()
	token, device, status := server.phoneDevices.redeem(code, "stream", "192.0.2.3:1000")
	if status != http.StatusOK {
		t.Fatalf("redeem status=%d", status)
	}
	entered := make(chan struct{})
	finished := make(chan struct{})
	guarded := server.phoneSessionGuard(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(finished)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(context.Background())
	request.Header.Set("Authorization", "Bearer "+token)
	go guarded.ServeHTTP(httptest.NewRecorder(), request)
	<-entered
	if !server.phoneDevices.revoke(device.ID) {
		t.Fatal("device was not revoked")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel the authenticated stream")
	}
}
