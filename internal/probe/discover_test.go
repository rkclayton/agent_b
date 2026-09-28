package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"harness/internal/config"
)

func TestDiscoveryCandidatesStayOnTypedHost(t *testing.T) {
	candidates, host, _, err := discoveryCandidates("example.test")
	if err != nil || host != "example.test" {
		t.Fatalf("host=%q err=%v", host, err)
	}
	for _, candidate := range candidates {
		parsed, parseErr := url.Parse(candidate)
		if parseErr != nil || parsed.Hostname() != host {
			t.Fatalf("candidate escaped typed host: %q (%v)", candidate, parseErr)
		}
	}
	joined := strings.Join(candidates, "\n")
	for _, want := range []string{"https://example.test:443", "http://example.test:8000/v1", "http://example.test:5000"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, candidates)
		}
	}
}

func TestDiscoverEndpointUsesExactURLAndListsModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"second"},{"id":"first"}]}`)
	}))
	defer server.Close()
	connection := config.Defaults(t.TempDir()).Connections[0]
	connection.BaseURL = server.URL
	result, err := DiscoverEndpoint(context.Background(), &connection)
	if err != nil {
		t.Fatal(err)
	}
	if result.BaseURL != server.URL || fmt.Sprint(result.Models) != "[first second]" {
		t.Fatalf("result=%+v", result)
	}
	if len(result.Attempts) != 1 || !result.Attempts[0].Allowed {
		t.Fatalf("attempts=%+v", result.Attempts)
	}
}

func TestDiscoverEndpointRefusesMalformedSchemeBeforeRequests(t *testing.T) {
	connection := config.Defaults(t.TempDir()).Connections[0]
	connection.BaseURL = "https:/host"
	result, err := DiscoverEndpoint(context.Background(), &connection)
	if err == nil || !strings.Contains(err.Error(), "two slashes") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(result.Attempts) != 1 || result.Attempts[0].Allowed {
		t.Fatalf("attempts=%+v", result.Attempts)
	}
}

// Item 2nb (a): THE TYPED ADDRESS WINS. This is the operator's case, 2026-09-27: he
// typed an address on :8080 and pressed Test. That server answers 401 without a key
// and lists three models with one. The walk treated the 401 as a failure, moved on,
// reached an :11434 that answered `{"object":"list","data":null}` — nothing loaded —
// and took THAT as the discovery, rewriting the port he typed and showing no models.
//
// A port he typed that answers at all now ends the walk, and an answer with no models
// never beats an answer with models.
func TestTheTypedAddressWinsWhateverItAnswers(t *testing.T) {
	for _, one := range []struct {
		name     string
		status   int
		body     string
		needsKey bool
		noAPI    bool
		wantErr  bool
		message  string
		models   int
	}{
		{
			name:   "401 is a server saying who it is, not a server that is not there",
			status: 401, body: `{"error":"unauthorized"}`,
			needsKey: true, wantErr: true, message: "wants an API key",
		},
		{
			name:   "403 is the same answer by another number",
			status: 403, body: `{"error":"forbidden"}`,
			needsKey: true, wantErr: true, message: "wants an API key",
		},
		{
			name:   "404 says there is no model API here, and which paths were tried",
			status: 404, body: `not found`,
			noAPI: true, wantErr: true, message: "No model API at",
		},
		{
			name:   "a list with models is taken, and the typed address is kept",
			status: 200, body: `{"object":"list","data":[{"id":"a"},{"id":"b"},{"id":"c"}]}`,
			models: 3,
		},
		{
			name:   "data null is an answer with nothing loaded, and it is reported as such",
			status: 200, body: `{"object":"list","data":null}`,
			models: 0,
		},
	} {
		t.Run(one.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("content-type", "application/json")
				w.WriteHeader(one.status)
				_, _ = w.Write([]byte(one.body))
			}))
			defer server.Close()

			// The address as he would type it: host and the port he means.
			hostPort := strings.TrimPrefix(server.URL, "http://")
			connection := &config.Connection{ID: "typed", BaseURL: server.URL, RequestTimeoutS: 2}
			result, err := DiscoverEndpoint(context.Background(), connection)

			if one.wantErr {
				if err == nil {
					t.Fatalf("the walk did not stop: %+v", result)
				}
				if !strings.Contains(err.Error(), one.message) {
					t.Errorf("the message does not say what happened: %v", err)
				}
			} else if err != nil {
				t.Fatalf("the walk failed: %v", err)
			}
			if result.NeedsKey != one.needsKey {
				t.Errorf("needs_key = %v", result.NeedsKey)
			}
			if result.NoModelAPI != one.noAPI {
				t.Errorf("no_model_api = %v", result.NoModelAPI)
			}
			if len(result.Models) != one.models {
				t.Errorf("models = %v", result.Models)
			}
			// Whatever it answered, the walk stopped ON THE TYPED ADDRESS and never
			// went looking at another port. That is the whole of (a).
			if result.TypedAddress == "" {
				t.Fatalf("the typed address was not recorded: %+v", result)
			}
			if !strings.Contains(result.TypedAddress, hostPort) {
				t.Errorf("the walk moved off the typed port to %q", result.TypedAddress)
			}
			answered := 0
			for _, attempt := range result.Attempts {
				if attempt.Answered {
					answered++
				}
				// Both schemes of the typed PORT are fair game — that is path and scheme
				// discovery on the address he gave. What must not appear is another port.
				if !strings.Contains(attempt.BaseURL, hostPort) {
					t.Errorf("the walk tried %q, which is not the port he typed", attempt.BaseURL)
				}
			}
			if answered == 0 {
				t.Errorf("no attempt was recorded as having answered: %+v", result.Attempts)
			}
			if !one.wantErr && result.BaseURL == "" {
				t.Errorf("a successful walk named no address: %+v", result)
			}
		})
	}
}

// (b): each attempt carries what it found, so the sheet can say it as it lands rather
// than sitting idle. A status is recorded whenever the address answered at all.
func TestEveryDiscoveryAttemptSaysWhatItFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()
	result, _ := DiscoverEndpoint(context.Background(), &config.Connection{ID: "typed", BaseURL: server.URL, RequestTimeoutS: 2})
	if len(result.Attempts) == 0 {
		t.Fatal("no attempts were recorded")
	}
	for _, attempt := range result.Attempts {
		if strings.TrimSpace(attempt.Result) == "" {
			t.Errorf("an attempt recorded no result: %+v", attempt)
		}
		if attempt.Answered && attempt.Status == 0 {
			t.Errorf("an attempt answered but carries no status: %+v", attempt)
		}
	}
	if result.Attempts[0].Result != "wants an API key" {
		t.Errorf("the first attempt reads %q", result.Attempts[0].Result)
	}
}

// An address that does not answer at all still lets the walk go on: that is the case
// the walk exists for, and (a) narrows it rather than removing it.
func TestAnAddressThatDoesNotAnswerLetsTheWalkContinue(t *testing.T) {
	// A port nothing is listening on: closed immediately, so the attempt does not
	// answer and is not allowed to end the walk.
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	address := closed.URL
	closed.Close()
	result, err := DiscoverEndpoint(context.Background(), &config.Connection{ID: "typed", BaseURL: address, RequestTimeoutS: 1})
	// Whether the walk ends up finding something on another port of this machine is
	// not the point; that the SILENT typed port did not end it is.
	_ = err
	if result.NeedsKey || result.NoModelAPI {
		t.Errorf("a silent address was classified as an answer: %+v", result)
	}
	if result.TypedAddress != "" {
		t.Errorf("a silent address was taken as the typed answer: %q", result.TypedAddress)
	}
	// Only the TYPED address is asserted about. The walk goes on to the ordinary
	// candidate ports, and on a developer's machine something may well be listening on
	// one of them — which is the walk working, not a fault.
	typedAttempts := 0
	for _, attempt := range result.Attempts {
		if !strings.HasPrefix(attempt.BaseURL, address) {
			continue
		}
		typedAttempts++
		if attempt.Answered {
			t.Errorf("an attempt on the closed typed port claims to have answered: %+v", attempt)
		}
	}
	if typedAttempts == 0 {
		t.Error("the typed address was never tried")
	}
}
