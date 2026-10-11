package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const provider = "devin-openai"
const gateway = "https://server.codeium.com/api/v1"

type authFile struct {
	Type         string `json:"type"`
	APIKey       string `json:"api_key"`
	InferenceURL string `json:"inference_url,omitempty"`
	Label        string `json:"label,omitempty"`
	ProxyURL     string `json:"proxy_url,omitempty"`
	Disabled     bool   `json:"disabled,omitempty"`
}
type authData struct {
	Provider, ID, FileName, Label, Prefix, ProxyURL string
	Disabled                                        bool
	StorageJSON                                     []byte
	Metadata                                        map[string]any
	Attributes                                      map[string]string
}
type request struct {
	Provider, AuthProvider, AuthID, FileName, State string
	RawJSON, StorageJSON                            []byte
	Attributes                                      map[string]string
	Host                                            struct{ AuthDir string }
	HostCallbackID                                  string `json:"host_callback_id"`
}
type httpRequest struct {
	Callback string              `json:"host_callback_id"`
	Method   string              `json:"method"`
	URL      string              `json:"url"`
	Headers  map[string][]string `json:"headers"`
	Body     []byte              `json:"body,omitempty"`
}
type httpResponse struct {
	StatusCode int
	Body       []byte
}
type login struct {
	verifier, authDir string
	expires           time.Time
	exchanging        bool
}
type plugin struct {
	mu     sync.Mutex
	logins map[string]login
	call   func(string, []byte) ([]byte, error)
}

func newPlugin(call func(string, []byte) ([]byte, error)) *plugin {
	return &plugin{logins: make(map[string]login), call: call}
}
func parseAuth(raw []byte, filename string) (authData, error) {
	var f authFile
	if json.Unmarshal(raw, &f) != nil {
		return authData{}, errors.New("invalid Devin auth JSON")
	}
	if f.Type != provider {
		return authData{}, errors.New("auth type must be devin-openai")
	}
	if strings.TrimSpace(f.APIKey) == "" || strings.ContainsAny(f.APIKey, "\r\n") {
		return authData{}, errors.New("Devin api_key is required and must be a single line")
	}
	base := gateway
	if f.InferenceURL != "" {
		u, err := url.Parse(f.InferenceURL)
		if err != nil {
			return authData{}, errors.New("invalid inference_url")
		}
		ip := net.ParseIP(u.Hostname())
		local := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
		if u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && local)) {
			return authData{}, errors.New("inference_url must be an HTTPS origin (HTTP allowed only on loopback)")
		}
		base = strings.TrimRight(u.String(), "/") + "/api/v1"
	}
	sum := sha256.Sum256([]byte(f.APIKey))
	id := provider + "-" + hex.EncodeToString(sum[:12])
	if filename == "" {
		filename = id + ".json"
	}
	storage, err := json.Marshal(f)
	if err != nil {
		return authData{}, err
	}
	return authData{Provider: provider, ID: filename, FileName: filename, Label: f.Label, Prefix: provider, ProxyURL: f.ProxyURL, Disabled: f.Disabled, StorageJSON: storage, Metadata: map[string]any{"type": provider}, Attributes: map[string]string{"base_url": base, "api_key": f.APIKey}}, nil
}
func (p *plugin) handle(method string, raw []byte) (any, error) {
	var r request
	if len(raw) > 0 && json.Unmarshal(raw, &r) != nil {
		return nil, errors.New("invalid plugin request JSON")
	}
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return map[string]any{"schema_version": 6, "metadata": map[string]any{"Name": provider, "Version": "0.2.0", "Author": "PomoAi", "GitHubRepository": "https://github.com/PomoAi-ai/cliproxyapi-plugin-devin-openai", "ConfigFields": []any{}}, "capabilities": map[string]bool{"auth_provider": true, "model_provider": true, "quota_provider": true}}, nil
	case "plugin.quiesce", "plugin.shutdown":
		p.mu.Lock()
		clear(p.logins)
		p.mu.Unlock()
		return struct{}{}, nil
	case "auth.identifier":
		return map[string]string{"identifier": provider}, nil
	case "quota.identifier", "quota.describe", "quota.fetch", "quota.reset":
		return p.quota(method, raw)
	case "auth.parse":
		var kind struct{ Type string }
		if json.Unmarshal(r.RawJSON, &kind) != nil {
			if r.Provider != provider {
				return map[string]bool{"Handled": false}, nil
			}
			return nil, errors.New("invalid Devin auth JSON")
		}
		if kind.Type != provider {
			return map[string]bool{"Handled": false}, nil
		}
		a, err := parseAuth(r.RawJSON, r.FileName)
		if err != nil {
			return nil, err
		}
		return map[string]any{"Handled": true, "Auth": a}, nil
	case "auth.login.start":
		return p.start(r)
	case "auth.login.poll":
		return p.poll(r)
	case "auth.refresh":
		return nil, errors.New("Devin session tokens cannot be refreshed; sign in again")
	case "model.static":
		return map[string]any{"Provider": provider, "Models": []any{}}, nil
	case "model.for_auth":
		if r.AuthProvider != provider {
			return map[string]any{"Models": []any{}}, nil
		}
		a, err := parseAuth(r.StorageJSON, "")
		if err != nil {
			return nil, err
		}
		body, err := p.http(r.HostCallbackID, "GET", a.Attributes["base_url"]+"/models", a.Attributes["api_key"], nil)
		if err != nil {
			return nil, err
		}
		models, err := parseModels(body)
		if err != nil {
			return nil, err
		}
		return map[string]any{"Provider": provider, "Models": models}, nil
	default:
		return nil, errors.New("unsupported plugin method")
	}
}
func randomString() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func (p *plugin) start(r request) (any, error) {
	if r.Provider != provider {
		return nil, errors.New("login provider must be devin-openai")
	}
	if r.Host.AuthDir == "" {
		return nil, errors.New("host AuthDir is required for OAuth callback")
	}
	state, err := randomString()
	if err != nil {
		return nil, err
	}
	verifier, err := randomString()
	if err != nil {
		return nil, err
	}
	challenge := sha256.Sum256([]byte(verifier))
	expires := time.Now().Add(10 * time.Minute)
	p.mu.Lock()
	for k, v := range p.logins {
		if time.Now().After(v.expires) {
			delete(p.logins, k)
		}
	}
	p.logins[state] = login{verifier: verifier, authDir: r.Host.AuthDir, expires: expires}
	p.mu.Unlock()
	q := url.Values{"state": {state}, "prompt": {"select_account"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	return map[string]any{"Provider": provider, "URL": "https://app.devin.ai/auth/cli/continue?" + q.Encode(), "State": state, "ExpiresAt": expires}, nil
}
func (p *plugin) poll(r request) (any, error) {
	if r.Provider != provider {
		return nil, errors.New("login provider must be devin-openai")
	}
	p.mu.Lock()
	flow, ok := p.logins[r.State]
	if !ok {
		p.mu.Unlock()
		return nil, errors.New("unknown or consumed login state; start login again")
	}
	if flow.exchanging {
		p.mu.Unlock()
		return map[string]string{"Status": "pending"}, nil
	}
	if time.Now().After(flow.expires) {
		delete(p.logins, r.State)
		p.mu.Unlock()
		return nil, errors.New("login expired; start login again")
	}
	path := filepath.Join(flow.authDir, ".oauth-"+provider+"-"+r.State+".oauth")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		p.mu.Unlock()
		return map[string]string{"Status": "pending"}, nil
	}
	flow.exchanging = true
	p.logins[r.State] = flow
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.logins, r.State); p.mu.Unlock() }()
	if err != nil {
		return nil, errors.New("cannot read OAuth callback file")
	}
	if err = os.Remove(path); err != nil {
		return nil, errors.New("cannot remove consumed OAuth callback file")
	}
	var cb struct{ Code, State, Error string }
	if json.Unmarshal(data, &cb) != nil || cb.State != r.State || cb.Code == "" || cb.Error != "" {
		return nil, errors.New("invalid or rejected OAuth callback; start login again")
	}
	body, _ := json.Marshal(map[string]string{"code": cb.Code, "code_verifier": flow.verifier})
	response, err := p.http(r.HostCallbackID, "POST", "https://api.devin.ai/auth/cli/token", "", body)
	if err != nil {
		return nil, err
	}
	var token struct{ Token string }
	if json.Unmarshal(response, &token) != nil || strings.TrimSpace(token.Token) == "" {
		return nil, errors.New("token exchange returned no valid token")
	}
	storage, _ := json.Marshal(authFile{Type: provider, APIKey: "devin-session-token$" + token.Token})
	a, err := parseAuth(storage, "")
	if err != nil {
		return nil, err
	}
	return map[string]any{"Status": "success", "Auth": a}, nil
}
func (p *plugin) http(callback, method, address, key string, body []byte) ([]byte, error) {
	if callback == "" {
		return nil, errors.New("host HTTP callback context is required")
	}
	headers := map[string][]string{"Accept": {"application/json"}}
	if key != "" {
		headers["Authorization"] = []string{"Bearer " + key}
	}
	if body != nil {
		headers["Content-Type"] = []string{"application/json"}
	}
	raw, _ := json.Marshal(httpRequest{callback, method, address, headers, body})
	result, err := p.call("host.http.do", raw)
	if err != nil {
		return nil, errors.New("Devin host HTTP request failed")
	}
	var resp httpResponse
	if json.Unmarshal(result, &resp) != nil {
		return nil, errors.New("invalid host HTTP response")
	}
	if resp.StatusCode == 401 {
		return nil, errors.New("Devin gateway HTTP 401: sign in again")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Devin upstream HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

type model struct {
	ID, Object, OwnedBy, DisplayName   string
	InputTokenLimit, OutputTokenLimit  int64
	ContextLength, MaxCompletionTokens int64
	UserDefined                        bool
}

func parseModels(body []byte) ([]model, error) {
	var list struct {
		Data []struct {
			ID       string                       `json:"id"`
			Owner    string                       `json:"owned_by"`
			Input    int64                        `json:"max_input_tokens"`
			Output   int64                        `json:"max_output_tokens"`
			Variants []struct{ Name, UID string } `json:"available_variants"`
		}
	}
	if json.Unmarshal(body, &list) != nil || list.Data == nil {
		return nil, errors.New("invalid Devin model catalog")
	}
	models := make([]model, 0, len(list.Data))
	seen := map[string]int{}
	for _, m := range list.Data {
		if strings.TrimSpace(m.ID) == "" || m.Input < 0 || m.Output < 0 {
			return nil, errors.New("invalid Devin model entry")
		}
		if _, exists := seen[m.ID]; !exists {
			seen[m.ID] = len(models)
			// The gateway owns effort validation; absent capability data must not strip caller intent.
			models = append(models, model{ID: m.ID, Object: "model", OwnedBy: m.Owner, DisplayName: m.ID, InputTokenLimit: m.Input, OutputTokenLimit: m.Output, ContextLength: m.Input, MaxCompletionTokens: m.Output, UserDefined: true})
		}
	}
	for _, m := range list.Data {
		for _, v := range m.Variants {
			if strings.TrimSpace(v.UID) == "" || strings.TrimSpace(v.Name) == "" {
				return nil, errors.New("invalid Devin model variant")
			}
			if index, exists := seen[v.UID]; exists {
				models[index].DisplayName = v.Name
			} else {
				seen[v.UID] = len(models)
				models = append(models, model{ID: v.UID, Object: "model", OwnedBy: m.Owner, DisplayName: v.Name, InputTokenLimit: m.Input, OutputTokenLimit: m.Output, ContextLength: m.Input, MaxCompletionTokens: m.Output, UserDefined: true})
			}
		}
	}
	return models, nil
}
