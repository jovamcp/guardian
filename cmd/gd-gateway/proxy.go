// Manejadores: autenticación, /v1/models, proxy de /v1/* con streaming, límites y contabilidad.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Gateway struct {
	cfg    *Config
	store  *Store
	router *Router
	audit  *Auditor
	client *http.Client
	rl     *rateLimiter
}

type rateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (r *rateLimiter) allow(key string, rpm int) bool {
	if rpm <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	cut := now.Add(-time.Minute)
	h := r.hits[key]
	n := 0
	for _, t := range h {
		if t.After(cut) {
			h[n] = t
			n++
		}
	}
	h = h[:n]
	if len(h) >= rpm {
		r.hits[key] = h
		return false
	}
	r.hits[key] = append(h, now)
	return true
}

func writeErr(w http.ResponseWriter, code int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": code}})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func (g *Gateway) authKey(w http.ResponseWriter, r *http.Request) *Key {
	tok := bearer(r)
	if tok == "" {
		writeErr(w, 401, "authentication_error", "falta la cabecera Authorization: Bearer <llave>")
		return nil
	}
	k := g.store.Lookup(tok)
	if k == nil {
		writeErr(w, 401, "authentication_error", "llave inválida, revocada o caducada")
		return nil
	}
	return k
}

func (k *Key) allows(model string) bool {
	for _, m := range k.Models {
		if m == "*" || m == model {
			return true
		}
	}
	return false
}

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	k := g.authKey(w, r)
	if k == nil {
		return
	}
	var data []map[string]any
	for _, m := range g.router.Models() {
		if !k.allows(m) {
			continue
		}
		if g.router.IsCloudModel(m) && !k.Cloud {
			continue
		}
		data = append(data, map[string]any{"id": m, "object": "model", "owned_by": "guardian", "cloud": g.router.IsCloudModel(m)})
	}
	if data == nil {
		data = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

type usageInfo struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// handleProxy reenvía /v1/chat/completions, /v1/completions y /v1/embeddings.
func (g *Gateway) handleProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	k := g.authKey(w, r)
	if k == nil {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeErr(w, 400, "invalid_request_error", "no se pudo leer el cuerpo")
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, 400, "invalid_request_error", "el cuerpo debe ser JSON")
		return
	}
	model, _ := req["model"].(string)
	if model == "" {
		writeErr(w, 400, "invalid_request_error", "falta \"model\"")
		return
	}
	stream, _ := req["stream"].(bool)
	callType := strings.TrimPrefix(r.URL.Path, "/v1/")
	ev := &auditEvent{ID: fmt.Sprintf("gd-%d", start.UnixNano()), Model: model, CallType: callType,
		KeyAlias: k.Alias, KeyID: k.ID, ClientIP: clientIP(r), StartTime: start}

	if !k.allows(model) {
		ev.fail(403, "key_model_access_denied")
		g.finish(k, ev, w, 403, "key_model_access_denied",
			fmt.Sprintf("la llave %q no puede usar el modelo %q (permitidos: %s)", k.Alias, model, strings.Join(k.Models, ",")))
		return
	}
	cloud := g.router.IsCloudModel(model)
	ev.Cloud = cloud
	if cloud && !k.Cloud {
		ev.fail(403, "cloud_not_allowed")
		g.finish(k, ev, w, 403, "cloud_not_allowed", fmt.Sprintf("la llave %q no tiene permiso para modelos cloud", k.Alias))
		return
	}
	rpm := k.RPM
	if rpm == 0 {
		rpm = g.cfg.DefaultRPM
	}
	if !g.rl.allow(k.ID, rpm) {
		ev.fail(429, "rate_limited")
		w.Header().Set("Retry-After", "60")
		g.finish(k, ev, w, 429, "rate_limit_error", fmt.Sprintf("límite de %d peticiones/min para %q", rpm, k.Alias))
		return
	}
	if cloud {
		snap := g.store.UsageSnapshot()
		if g.cfg.CloudBudgetUSD > 0 && snap.CloudUSD >= g.cfg.CloudBudgetUSD {
			ev.fail(402, "budget_exceeded")
			g.finish(k, ev, w, 402, "budget_exceeded", fmt.Sprintf("presupuesto cloud mensual agotado (%.2f/%.2f USD)", snap.CloudUSD, g.cfg.CloudBudgetUSD))
			return
		}
		if ku := snap.Keys[k.ID]; k.BudgetUSD > 0 && ku != nil && ku.USD >= k.BudgetUSD {
			ev.fail(402, "key_budget_exceeded")
			g.finish(k, ev, w, 402, "budget_exceeded", fmt.Sprintf("presupuesto mensual de la llave %q agotado (%.2f/%.2f USD)", k.Alias, ku.USD, k.BudgetUSD))
			return
		}
	}
	cands := g.router.Candidates(model)
	if len(cands) == 0 {
		ev.fail(404, "model_not_found")
		g.finish(k, ev, w, 404, "model_not_found", fmt.Sprintf("ningún nodo sirve el modelo %q", model))
		return
	}
	// Pedir siempre el uso en streaming (OpenAI lo exige explícitamente; Ollama lo acepta).
	if stream && callType == "chat/completions" {
		if _, ok := req["stream_options"]; !ok {
			req["stream_options"] = map[string]any{"include_usage": true}
			body, _ = json.Marshal(req)
		}
	}

	var lastErr error
	for i, u := range cands {
		ev.Upstream = u.Name
		resp, err := g.forward(r.Context(), u, r.URL.Path, body)
		if err != nil {
			lastErr = err
			u.healthy.Store(false)
			u.lastErr.Store(err.Error())
			continue
		}
		if resp.StatusCode >= 500 {
			// El nodo responde pero está roto: se marca no sano (el chequeo periódico lo recupera)
			// y se prueba el siguiente; si era el último, se devuelve tal cual.
			u.healthy.Store(false)
			u.lastErr.Store(fmt.Sprintf("HTTP %d", resp.StatusCode))
			if i < len(cands)-1 {
				resp.Body.Close()
				lastErr = fmt.Errorf("%s respondió %d", u.Name, resp.StatusCode)
				continue
			}
		}
		g.relay(w, resp, u, k, ev, stream)
		return
	}
	ev.fail(502, "upstream_unavailable")
	g.finish(k, ev, w, 502, "upstream_unavailable", fmt.Sprintf("ningún nodo disponible para %q: %v", model, lastErr))
}

func (g *Gateway) forward(ctx context.Context, u *Upstream, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if u.Type == "openai" && u.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+u.apiKey)
	}
	return g.client.Do(req)
}

// relay copia la respuesta al cliente (SSE incluido) y extrae el uso para la contabilidad.
func (g *Gateway) relay(w http.ResponseWriter, resp *http.Response, u *Upstream, k *Key, ev *auditEvent, stream bool) {
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("X-Guardian-Upstream", u.Name)
	w.WriteHeader(resp.StatusCode)
	ev.Status = resp.StatusCode
	var usage usageInfo
	if stream && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		fl, _ := w.(http.Flusher)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if bytes.HasPrefix(line, []byte("data: ")) && bytes.Contains(line, []byte(`"usage"`)) {
				var chunk struct {
					Usage *usageInfo `json:"usage"`
				}
				if json.Unmarshal(line[6:], &chunk) == nil && chunk.Usage != nil {
					usage = *chunk.Usage
				}
			}
			w.Write(line)
			w.Write([]byte("\n"))
			if fl != nil {
				fl.Flush()
			}
		}
	} else {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		w.Write(raw)
		var out struct {
			Usage *usageInfo `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &out) == nil {
			if out.Usage != nil {
				usage = *out.Usage
			}
			if out.Error != nil {
				ev.Error = out.Error.Message
			}
		}
	}
	ev.PromptTokens, ev.CompletionTokens = usage.PromptTokens, usage.CompletionTokens
	ev.TotalTokens = usage.TotalTokens
	if ev.TotalTokens == 0 {
		ev.TotalTokens = ev.PromptTokens + ev.CompletionTokens
	}
	if p, ok := u.priceFor(ev.Model); ok {
		ev.Cost = (float64(ev.PromptTokens)*p.Input + float64(ev.CompletionTokens)*p.Output) / 1e6
	}
	failed := resp.StatusCode >= 400
	total, ku := g.store.Record(k.ID, ev.PromptTokens, ev.CompletionTokens, ev.Cost, ev.Cloud, failed)
	ev.EndTime = time.Now()
	g.audit.send(ev)
	g.checkBudgetWarning(total, k, ku)
}

// finish registra un rechazo (sin llamada al upstream) y responde el error.
func (g *Gateway) finish(k *Key, ev *auditEvent, w http.ResponseWriter, code int, typ, msg string) {
	ev.EndTime = time.Now()
	g.store.Record(k.ID, 0, 0, 0, false, true)
	g.audit.send(ev)
	writeErr(w, code, typ, msg)
}

func (g *Gateway) checkBudgetWarning(totalCloudUSD float64, k *Key, ku *KeyUsage) {
	if g.cfg.CloudBudgetUSD <= 0 {
		return
	}
	snap := g.store.UsageSnapshot()
	if !snap.Warned80 && totalCloudUSD >= 0.8*g.cfg.CloudBudgetUSD {
		g.store.MarkWarned()
		g.audit.send(&auditEvent{ID: fmt.Sprintf("gd-budget-%d", time.Now().UnixNano()), Event: "budget",
			Model: "-", CallType: "budget_warning", KeyAlias: k.Alias, KeyID: k.ID, Cloud: true,
			Cost: totalCloudUSD, StartTime: time.Now(), EndTime: time.Now(), Status: 200,
			Error: fmt.Sprintf("gasto cloud mensual %.2f USD ≥ 80%% del presupuesto %.2f USD", totalCloudUSD, g.cfg.CloudBudgetUSD)})
	}
}

// ---------------------------------------------------------------- administración

func (g *Gateway) requireMaster(w http.ResponseWriter, r *http.Request) bool {
	if subtleEqual(bearer(r), g.cfg.MasterKey) {
		return true
	}
	writeErr(w, 401, "authentication_error", "se requiere la master key")
	return false
}

func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

type createKeyReq struct {
	Alias     string            `json:"alias"`
	Models    []string          `json:"models"`
	RPM       int               `json:"rpm"`
	BudgetUSD float64           `json:"budget_usd"`
	Cloud     bool              `json:"cloud"`
	Duration  string            `json:"duration"` // 30m, 24h, 30d
	Metadata  map[string]string `json:"metadata"`
}

func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func (g *Gateway) handleAdminKeys(w http.ResponseWriter, r *http.Request) {
	if !g.requireMaster(w, r) {
		return
	}
	switch r.Method {
	case http.MethodPost:
		var req createKeyReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Alias == "" || len(req.Models) == 0 {
			writeErr(w, 400, "invalid_request_error", "alias y models son obligatorios")
			return
		}
		ttl, err := parseDuration(req.Duration)
		if err != nil {
			writeErr(w, 400, "invalid_request_error", "duration inválida (30m, 24h, 30d)")
			return
		}
		k, secret, err := g.store.CreateKey(req.Alias, req.Models, req.RPM, req.BudgetUSD, req.Cloud, ttl, req.Metadata)
		if err != nil {
			writeErr(w, 409, "conflict", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"key": secret, "id": k.ID, "alias": k.Alias, "models": k.Models,
			"rpm": k.RPM, "budget_usd": k.BudgetUSD, "cloud": k.Cloud, "expires_at": k.ExpiresAt})
	case http.MethodGet:
		usage := g.store.UsageSnapshot()
		var out []map[string]any
		for _, k := range g.store.ListKeys() {
			ku := usage.Keys[k.ID]
			if ku == nil {
				ku = &KeyUsage{}
			}
			out = append(out, map[string]any{"id": k.ID, "alias": k.Alias, "prefix": k.Prefix, "models": k.Models,
				"rpm": k.RPM, "budget_usd": k.BudgetUSD, "cloud": k.Cloud, "created_at": k.CreatedAt, "expires_at": k.ExpiresAt,
				"usage": ku})
		}
		if out == nil {
			out = []map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"keys": out, "month": usage.Month})
	case http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/admin/keys/")
		if id == "" || id == r.URL.Path {
			writeErr(w, 400, "invalid_request_error", "indica /admin/keys/<id|alias|prefijo>")
			return
		}
		if err := g.store.Revoke(id); err != nil {
			writeErr(w, 404, "not_found", err.Error())
			return
		}
		w.WriteHeader(204)
	default:
		writeErr(w, 405, "method_not_allowed", r.Method)
	}
}

func (g *Gateway) handleAdminHealth(w http.ResponseWriter, r *http.Request) {
	if !g.requireMaster(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"upstreams": g.router.Status(), "models": g.router.Models()})
}

func (g *Gateway) handleAdminUsage(w http.ResponseWriter, r *http.Request) {
	if !g.requireMaster(w, r) {
		return
	}
	u := g.store.UsageSnapshot()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"month": u.Month, "cloud_usd": u.CloudUSD,
		"cloud_budget_usd": g.cfg.CloudBudgetUSD, "keys": u.Keys})
}

func (g *Gateway) handleHealth(w http.ResponseWriter, _ *http.Request) {
	anyHealthy := false
	for _, s := range g.router.Status() {
		if s.Healthy {
			anyHealthy = true
		}
	}
	if !anyHealthy {
		writeErr(w, 503, "unavailable", "ningún upstream sano")
		return
	}
	w.Write([]byte("ok\n"))
}

var errNoBody = errors.New("sin cuerpo")
