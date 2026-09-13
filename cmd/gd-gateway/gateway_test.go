package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeOllama simula /api/tags y /v1/chat/completions (normal y streaming) con usage.
func fakeOllama(t *testing.T, name string, fail *atomic.Bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail != nil && fail.Load() {
			http.Error(w, "caído", 500)
			return
		}
		switch r.URL.Path {
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": "qwen2.5:0.5b"}, {"name": "llama3.1:8b"}}})
		case "/v1/models":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		case "/v1/chat/completions":
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("X-Upstream", name)
			if s, _ := req["stream"].(bool); s {
				w.Header().Set("Content-Type", "text/event-stream")
				fl := w.(http.Flusher)
				fmt.Fprintf(w, "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"Hola\"}}]}\n\n")
				fl.Flush()
				fmt.Fprintf(w, "data: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\ndata: [DONE]\n\n")
				fl.Flush()
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "c1", "model": req["model"], "upstream": name,
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "Hola"}}},
				"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}})
		default:
			http.NotFound(w, r)
		}
	}))
}

func newTestGateway(t *testing.T, ups []UpstreamConfig, budget float64) (*Gateway, *httptest.Server, []map[string]any) {
	t.Helper()
	var events []map[string]any
	auditSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var evs []map[string]any
		json.NewDecoder(r.Body).Decode(&evs)
		events = append(events, evs...)
	}))
	t.Cleanup(auditSrv.Close)
	dir := t.TempDir()
	cfg := &Config{Listen: ":0", DataDir: dir, AuditURL: auditSrv.URL, MasterKey: "sk-master-0123456789abcdef", Upstreams: ups, CloudBudgetUSD: budget, DefaultRPM: 60}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := newGateway(cfg, store)
	srv := httptest.NewServer(g.mux())
	t.Cleanup(srv.Close)
	return g, srv, events
}

func call(t *testing.T, srv *httptest.Server, method, path, key string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, raw
}

func createKey(t *testing.T, srv *httptest.Server, req createKeyReq) string {
	t.Helper()
	resp, raw := call(t, srv, "POST", "/admin/keys", "sk-master-0123456789abcdef", req)
	if resp.StatusCode != 200 {
		t.Fatalf("crear llave: %d %s", resp.StatusCode, raw)
	}
	var out struct{ Key string }
	json.Unmarshal(raw, &out)
	return out.Key
}

func chat(model string, stream bool) map[string]any {
	return map[string]any{"model": model, "stream": stream, "messages": []map[string]string{{"role": "user", "content": "hola"}}}
}

func TestAuthModelsAndProxy(t *testing.T) {
	up := fakeOllama(t, "a", nil)
	defer up.Close()
	_, srv, _ := newTestGateway(t, []UpstreamConfig{{Name: "a", Type: "ollama", URL: up.URL, Models: []string{"*"}}}, 0)

	if resp, _ := call(t, srv, "GET", "/v1/models", "", nil); resp.StatusCode != 401 {
		t.Fatalf("sin llave: %d", resp.StatusCode)
	}
	if resp, _ := call(t, srv, "GET", "/v1/models", "sk-gd-inventada", nil); resp.StatusCode != 401 {
		t.Fatalf("llave inválida: %d", resp.StatusCode)
	}
	if resp, _ := call(t, srv, "POST", "/admin/keys", "sk-otra", createKeyReq{Alias: "x", Models: []string{"*"}}); resp.StatusCode != 401 {
		t.Fatalf("admin sin master: %d", resp.StatusCode)
	}
	key := createKey(t, srv, createKeyReq{Alias: "app", Models: []string{"qwen2.5:0.5b"}, RPM: 3})
	if !strings.HasPrefix(key, "sk-gd-") {
		t.Fatalf("formato de llave: %s", key)
	}
	resp, raw := call(t, srv, "GET", "/v1/models", key, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), "qwen2.5:0.5b") || strings.Contains(string(raw), "llama3.1") {
		t.Fatalf("models filtrados: %d %s", resp.StatusCode, raw)
	}
	resp, raw = call(t, srv, "POST", "/v1/chat/completions", key, chat("qwen2.5:0.5b", false))
	if resp.StatusCode != 200 || !strings.Contains(string(raw), "Hola") || resp.Header.Get("X-Guardian-Upstream") != "a" {
		t.Fatalf("proxy: %d %s", resp.StatusCode, raw)
	}
	if resp, raw = call(t, srv, "POST", "/v1/chat/completions", key, chat("llama3.1:8b", false)); resp.StatusCode != 403 || !strings.Contains(string(raw), "key_model_access_denied") {
		t.Fatalf("modelo no permitido: %d %s", resp.StatusCode, raw)
	}
	// 3 rpm: la primera petición OK ya contó; dos más pasan; la cuarta 429.
	call(t, srv, "POST", "/v1/chat/completions", key, chat("qwen2.5:0.5b", false))
	call(t, srv, "POST", "/v1/chat/completions", key, chat("qwen2.5:0.5b", false))
	if resp, _ = call(t, srv, "POST", "/v1/chat/completions", key, chat("qwen2.5:0.5b", false)); resp.StatusCode != 429 {
		t.Fatalf("rpm: esperado 429, got %d", resp.StatusCode)
	}
	// Revocar
	if resp, _ = call(t, srv, "DELETE", "/admin/keys/app", "sk-master-0123456789abcdef", nil); resp.StatusCode != 204 {
		t.Fatalf("revocar: %d", resp.StatusCode)
	}
	if resp, _ = call(t, srv, "GET", "/v1/models", key, nil); resp.StatusCode != 401 {
		t.Fatalf("llave revocada: %d", resp.StatusCode)
	}
}

func TestStreamingUsageAndAudit(t *testing.T) {
	up := fakeOllama(t, "a", nil)
	defer up.Close()
	g, srv, _ := newTestGateway(t, []UpstreamConfig{{Name: "a", Type: "ollama", URL: up.URL, Models: []string{"qwen2.5:0.5b"}}}, 0)
	key := createKey(t, srv, createKeyReq{Alias: "stream", Models: []string{"*"}})
	b, _ := json.Marshal(chat("qwen2.5:0.5b", true))
	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type: %s", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	var lines []string
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 3 || lines[2] != "data: [DONE]" {
		t.Fatalf("stream: %v", lines)
	}
	time.Sleep(200 * time.Millisecond)
	u := g.store.UsageSnapshot()
	var ku *KeyUsage
	for _, v := range u.Keys {
		ku = v
	}
	if ku == nil || ku.PromptTokens != 7 || ku.CompletionTokens != 3 {
		t.Fatalf("uso en streaming no contabilizado: %+v", ku)
	}
}

func TestFailoverBetweenNodes(t *testing.T) {
	var failA atomic.Bool
	a := fakeOllama(t, "a", &failA)
	b := fakeOllama(t, "b", nil)
	defer a.Close()
	defer b.Close()
	_, srv, _ := newTestGateway(t, []UpstreamConfig{
		{Name: "a", Type: "ollama", URL: a.URL, Models: []string{"*"}},
		{Name: "b", Type: "ollama", URL: b.URL, Models: []string{"*"}},
	}, 0)
	key := createKey(t, srv, createKeyReq{Alias: "ha", Models: []string{"*"}})
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		resp, _ := call(t, srv, "POST", "/v1/chat/completions", key, chat("qwen2.5:0.5b", false))
		seen[resp.Header.Get("X-Guardian-Upstream")]++
	}
	if seen["a"] == 0 || seen["b"] == 0 {
		t.Fatalf("sin reparto entre nodos: %v", seen)
	}
	failA.Store(true)
	for i := 0; i < 4; i++ {
		resp, raw := call(t, srv, "POST", "/v1/chat/completions", key, chat("qwen2.5:0.5b", false))
		if resp.StatusCode != 200 || resp.Header.Get("X-Guardian-Upstream") != "b" {
			t.Fatalf("failover: %d %s %s", resp.StatusCode, resp.Header.Get("X-Guardian-Upstream"), raw)
		}
	}
	resp, raw := call(t, srv, "GET", "/admin/health", "sk-master-0123456789abcdef", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"healthy":false`) {
		t.Fatalf("admin/health debería reflejar el nodo caído: %s", raw)
	}
}

func TestCloudPermissionAndBudget(t *testing.T) {
	local := fakeOllama(t, "local", nil)
	cloud := fakeOllama(t, "cloud", nil)
	defer local.Close()
	defer cloud.Close()
	os.Setenv("GW_TEST_CLOUD_KEY", "sk-cloud")
	_, srv, _ := newTestGateway(t, []UpstreamConfig{
		{Name: "local", Type: "ollama", URL: local.URL, Models: []string{"qwen2.5:0.5b"}},
		{Name: "cloud", Type: "openai", URL: cloud.URL, APIKeyEnv: "GW_TEST_CLOUD_KEY", Cloud: true, Models: []string{"gpt-4o-mini"},
			Prices: map[string]Price{"gpt-4o-mini": {Input: 1_000_000, Output: 1_000_000}}}, // 1 USD por token para probar
	}, 10)
	plain := createKey(t, srv, createKeyReq{Alias: "plain", Models: []string{"*"}})
	rich := createKey(t, srv, createKeyReq{Alias: "rich", Models: []string{"*"}, Cloud: true, BudgetUSD: 100})

	if resp, raw := call(t, srv, "POST", "/v1/chat/completions", plain, chat("gpt-4o-mini", false)); resp.StatusCode != 403 || !strings.Contains(string(raw), "cloud_not_allowed") {
		t.Fatalf("llave sin cloud: %d %s", resp.StatusCode, raw)
	}
	if _, raw := call(t, srv, "GET", "/v1/models", plain, nil); strings.Contains(string(raw), "gpt-4o-mini") {
		t.Fatalf("la llave sin cloud no debe ver modelos cloud: %s", raw)
	}
	// 7 tokens × 1 USD = 7 USD por llamada; presupuesto global 10 → la segunda llamada supera el 80 % y la tercera da 402.
	resp, _ := call(t, srv, "POST", "/v1/chat/completions", rich, chat("gpt-4o-mini", false))
	if resp.StatusCode != 200 {
		t.Fatalf("cloud permitido: %d", resp.StatusCode)
	}
	resp, _ = call(t, srv, "POST", "/v1/chat/completions", rich, chat("gpt-4o-mini", false))
	if resp.StatusCode != 200 {
		t.Fatalf("segunda llamada cloud: %d", resp.StatusCode)
	}
	resp, raw := call(t, srv, "POST", "/v1/chat/completions", rich, chat("gpt-4o-mini", false))
	if resp.StatusCode != 402 || !strings.Contains(string(raw), "budget_exceeded") {
		t.Fatalf("presupuesto: %d %s", resp.StatusCode, raw)
	}
	// El modelo local sigue disponible aunque el cloud esté agotado.
	if resp, _ = call(t, srv, "POST", "/v1/chat/completions", rich, chat("qwen2.5:0.5b", false)); resp.StatusCode != 200 {
		t.Fatalf("local tras agotar cloud: %d", resp.StatusCode)
	}
	resp, raw = call(t, srv, "GET", "/admin/usage", "sk-master-0123456789abcdef", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"cloud_usd":14`) {
		t.Fatalf("usage: %s", raw)
	}
}

func TestKeysPersistAcrossRestart(t *testing.T) {
	up := fakeOllama(t, "a", nil)
	defer up.Close()
	dir := t.TempDir()
	cfg := &Config{DataDir: dir, MasterKey: "sk-master-0123456789abcdef", Upstreams: []UpstreamConfig{{Name: "a", Type: "ollama", URL: up.URL, Models: []string{"*"}}}, DefaultRPM: 60}
	s1, _ := openStore(dir)
	_, secret, err := s1.CreateKey("persist", []string{"*"}, 0, 0, false, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := openStore(dir)
	if s2.Lookup(secret) == nil {
		t.Fatal("la llave no sobrevivió al reinicio")
	}
	if raw, _ := os.ReadFile(dir + "/keys.json"); strings.Contains(string(raw), secret) {
		t.Fatal("la llave en claro está en disco")
	}
	_ = cfg
}
