// Eventos de auditoría hacia Vector (mismo esquema que emitía LiteLLM: lista de objetos JSON).
package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

type auditEvent struct {
	ID               string
	Event            string // vacío = llm_request; "budget" para avisos de presupuesto
	Model            string
	CallType         string
	KeyAlias         string
	KeyID            string
	ClientIP         string
	Upstream         string
	Cloud            bool
	Status           int
	Error            string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	Cost             float64
	StartTime        time.Time
	EndTime          time.Time
}

func (e *auditEvent) fail(code int, msg string) {
	e.Status = code
	e.Error = msg
}

func (e *auditEvent) payload() map[string]any {
	status := "success"
	if e.Status >= 400 {
		status = "failure"
	}
	event := e.Event
	if event == "" {
		event = "llm_request"
	}
	return map[string]any{
		"id": e.ID, "event": event, "model": e.Model, "call_type": e.CallType, "status": status,
		"http_status": e.Status, "error_str": e.Error,
		"metadata": map[string]any{"user_api_key_alias": e.KeyAlias, "user_api_key_id": e.KeyID, "requester_ip_address": e.ClientIP},
		"upstream": e.Upstream, "cloud": e.Cloud,
		"prompt_tokens": e.PromptTokens, "completion_tokens": e.CompletionTokens, "total_tokens": e.TotalTokens,
		"response_cost": e.Cost,
		"startTime":     float64(e.StartTime.UnixNano()) / 1e9,
		"endTime":       float64(e.EndTime.UnixNano()) / 1e9,
		"latency_ms":    e.EndTime.Sub(e.StartTime).Milliseconds(),
	}
}

type Auditor struct {
	url    string
	ch     chan map[string]any
	client *http.Client
}

func newAuditor(url string) *Auditor {
	a := &Auditor{url: url, ch: make(chan map[string]any, 1024), client: &http.Client{Timeout: 5 * time.Second}}
	go a.loop()
	return a
}

func (a *Auditor) send(e *auditEvent) {
	select {
	case a.ch <- e.payload():
	default:
		log.Printf("audit: cola llena, evento descartado (%s)", e.ID)
	}
}

func (a *Auditor) loop() {
	for ev := range a.ch {
		if a.url == "" {
			continue
		}
		body, _ := json.Marshal([]map[string]any{ev})
		resp, err := a.client.Post(a.url, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("audit: %v", err)
			continue
		}
		resp.Body.Close()
	}
}
