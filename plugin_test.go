package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func encoded(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}

// These checks catch accidental takeover of legacy accounts and credential routing drift.
func TestAuthIsolationAndGatewayValidation(t *testing.T) {
	p := newPlugin(nil)
	result, err := p.handle("auth.parse", encoded(request{RawJSON: []byte(`{"type":"devin","api_key":"old"}`)}))
	if err != nil || result.(map[string]bool)["Handled"] {
		t.Fatal("legacy auth claimed", err)
	}
	for _, origin := range []string{"https://gateway.example/path", "http://gateway.example", "https://user:pass@gateway.example", "https://gateway.example?token=secret"} {
		_, err := parseAuth(encoded(authFile{Type: provider, APIKey: "fake", InferenceURL: origin}), "")
		if err == nil {
			t.Fatalf("unsafe origin accepted: %s", origin)
		}
	}
	a, err := parseAuth(encoded(authFile{Type: provider, APIKey: "fake", InferenceURL: "http://127.0.0.1:8123"}), "example.json")
	if err != nil || a.Attributes["base_url"] != "http://127.0.0.1:8123/api/v1" || a.Prefix != provider {
		t.Fatal(a, err)
	}
}

// A cached/statically substituted catalog or a dropped variant would fail this test.
func TestCatalogTracksAccountResponse(t *testing.T) {
	catalog := `{"data":[{"id":"family","available_variants":[{"uid":"concrete","name":"High"}]}]}`
	p := newPlugin(func(method string, raw []byte) ([]byte, error) {
		var r httpRequest
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		if method != "host.http.do" || r.Callback != "context" || r.URL != gateway+"/models" || r.Headers["Authorization"][0] != "Bearer fake" {
			t.Fatal("incorrect host request")
		}
		return encoded(httpResponse{200, []byte(catalog)}), nil
	})
	r := request{AuthProvider: provider, StorageJSON: encoded(authFile{Type: provider, APIKey: "fake"}), HostCallbackID: "context"}
	got, err := p.handle("model.for_auth", encoded(r))
	if err != nil {
		t.Fatal(err)
	}
	models := got.(map[string]any)["Models"].([]model)
	if len(models) != 2 || models[1].ID != "concrete" {
		t.Fatal(models)
	}
	catalog = `{"data":[{"id":"new-model"}]}`
	got, err = p.handle("model.for_auth", encoded(r))
	if err != nil {
		t.Fatal(err)
	}
	models = got.(map[string]any)["Models"].([]model)
	if len(models) != 1 || models[0].ID != "new-model" {
		t.Fatal(models)
	}
	catalog = `{"error":"broken"}`
	if _, err = p.handle("model.for_auth", encoded(r)); err == nil {
		t.Fatal("invalid catalog masked")
	}
}

// Replay, expiry, state mismatch, or missing PKCE verification must not mint credentials.
func TestPKCECallbackConsumedOnce(t *testing.T) {
	calls := 0
	var challenge string
	p := newPlugin(func(_ string, raw []byte) ([]byte, error) {
		calls++
		var r httpRequest
		json.Unmarshal(raw, &r)
		var body map[string]string
		json.Unmarshal(r.Body, &body)
		hash := sha256.Sum256([]byte(body["code_verifier"]))
		if body["code"] != "fake-code" || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
			t.Fatal("PKCE mismatch")
		}
		return encoded(httpResponse{200, []byte(`{"token":"fake-token"}`)}), nil
	})
	r := request{Provider: provider, HostCallbackID: "context"}
	r.Host.AuthDir = t.TempDir()
	started, err := p.start(r)
	if err != nil {
		t.Fatal(err)
	}
	start := started.(map[string]any)
	r.State = start["State"].(string)
	u, _ := url.Parse(start["URL"].(string))
	challenge = u.Query().Get("code_challenge")
	if u.Query().Has("redirect_uri") {
		t.Fatal("manual login redirects")
	}
	pending, err := p.poll(r)
	if err != nil || pending.(map[string]string)["Status"] != "pending" {
		t.Fatal(pending, err)
	}
	path := filepath.Join(r.Host.AuthDir, ".oauth-"+provider+"-"+r.State+".oauth")
	if err = os.WriteFile(path, encoded(map[string]string{"code": "fake-code", "state": r.State}), 0600); err != nil {
		t.Fatal(err)
	}
	done, err := p.poll(r)
	if err != nil {
		t.Fatal(err)
	}
	if done.(map[string]any)["Auth"].(authData).Attributes["api_key"] != "devin-session-token$fake-token" {
		t.Fatal("wrong token")
	}
	if _, err = p.poll(r); err == nil || calls != 1 {
		t.Fatal("callback replayed")
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("callback retained")
	}
	p.logins["expired"] = login{expires: time.Now().Add(-time.Second)}
	r.State = "expired"
	if _, err = p.poll(r); err == nil || calls != 1 {
		t.Fatal("expired login used")
	}
	p.logins["mismatch"] = login{authDir: r.Host.AuthDir, expires: time.Now().Add(time.Minute)}
	r.State = "mismatch"
	path = filepath.Join(r.Host.AuthDir, ".oauth-"+provider+"-mismatch.oauth")
	if err = os.WriteFile(path, []byte(`{"state":"wrong","code":"fake-code"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = p.poll(r); err == nil || calls != 1 {
		t.Fatal("state mismatch accepted")
	}
}

func TestUpstreamErrorsDoNotLeakCredentials(t *testing.T) {
	for _, status := range []int{401, 403, 429, 503} {
		p := newPlugin(func(string, []byte) ([]byte, error) {
			return encoded(httpResponse{status, []byte(`secret-token fake-code`)}), nil
		})
		_, err := p.http("context", "GET", gateway+"/models", "fake", nil)
		if err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Fatal("unsafe error", err)
		}
		if status == 401 && !strings.Contains(err.Error(), "sign in again") {
			t.Fatal(err)
		}
	}
	p := newPlugin(func(string, []byte) ([]byte, error) { return nil, errors.New("secret-token") })
	_, err := p.http("context", "GET", gateway+"/models", "fake", nil)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatal(err)
	}
}

func TestOverlappingPollWaitsForTokenExchange(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p := newPlugin(func(string, []byte) ([]byte, error) {
		close(entered)
		<-release
		return encoded(httpResponse{200, []byte(`{"token":"fake-token"}`)}), nil
	})
	r := request{Provider: provider, State: "concurrent", HostCallbackID: "context"}
	r.Host.AuthDir = t.TempDir()
	p.logins[r.State] = login{authDir: r.Host.AuthDir, verifier: "fake-verifier", expires: time.Now().Add(time.Minute)}
	path := filepath.Join(r.Host.AuthDir, ".oauth-"+provider+"-"+r.State+".oauth")
	if err := os.WriteFile(path, encoded(map[string]string{"state": r.State, "code": "fake-code"}), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := p.poll(r); done <- err }()
	<-entered
	result, err := p.poll(r)
	close(release)
	if err != nil || result.(map[string]string)["Status"] != "pending" {
		t.Fatal("overlapping poll failed", result, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
