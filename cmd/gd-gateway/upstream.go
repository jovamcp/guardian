// Nodos upstream: salud, descubrimiento de modelos, selección con reparto y failover.
package main

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jovamcp/guardian/internal/yamlmini"
)

type Upstream struct {
	UpstreamConfig
	apiKey     string
	healthy    atomic.Bool
	lastCheck  atomic.Int64
	lastErr    atomic.Value // string
	discovered atomic.Value // []string
	rr         atomic.Uint64
}

func newUpstreams(cfgs []UpstreamConfig) []*Upstream {
	var out []*Upstream
	for _, c := range cfgs {
		u := &Upstream{UpstreamConfig: c}
		if c.APIKeyEnv != "" {
			u.apiKey = os.Getenv(c.APIKeyEnv)
		}
		u.healthy.Store(c.Type == "openai") // los cloud se asumen sanos hasta que fallen
		u.lastErr.Store("")
		u.discovered.Store([]string{})
		out = append(out, u)
	}
	return out
}

// Serves indica si el upstream ofrece el modelo (lista explícita o descubierto con "*").
func (u *Upstream) Serves(model string) bool {
	for _, m := range u.Models {
		if m == model {
			return true
		}
		if m == "*" {
			for _, d := range u.discovered.Load().([]string) {
				if d == model {
					return true
				}
			}
		}
	}
	return false
}

func (u *Upstream) AllModels() []string {
	set := map[string]bool{}
	for _, m := range u.Models {
		if m == "*" {
			for _, d := range u.discovered.Load().([]string) {
				set[d] = true
			}
			continue
		}
		set[m] = true
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func (u *Upstream) check(client *http.Client) {
	u.lastCheck.Store(time.Now().Unix())
	switch u.Type {
	case "ollama":
		resp, err := client.Get(u.URL + "/api/tags")
		if err != nil {
			u.healthy.Store(false)
			u.lastErr.Store(err.Error())
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			u.healthy.Store(false)
			u.lastErr.Store(resp.Status)
			return
		}
		var tags struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		json.NewDecoder(resp.Body).Decode(&tags)
		var names []string
		for _, m := range tags.Models {
			names = append(names, m.Name)
		}
		u.discovered.Store(names)
		u.healthy.Store(true)
		u.lastErr.Store("")
	case "openai":
		// Comprobación ligera: /v1/models con la credencial. 401/403 = credencial mala.
		req, _ := http.NewRequest("GET", u.URL+"/v1/models", nil)
		if u.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+u.apiKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			u.healthy.Store(false)
			u.lastErr.Store(err.Error())
			return
		}
		resp.Body.Close()
		ok := resp.StatusCode < 500 && resp.StatusCode != 401 && resp.StatusCode != 403
		u.healthy.Store(ok)
		if ok {
			u.lastErr.Store("")
		} else {
			u.lastErr.Store(resp.Status)
		}
	}
}

type Router struct {
	ups    []*Upstream
	client *http.Client
}

func newRouter(ups []*Upstream) *Router {
	r := &Router{ups: ups, client: &http.Client{Timeout: 8 * time.Second}}
	for _, u := range ups {
		u.check(r.client)
	}
	go func() {
		t := time.NewTicker(30 * time.Second)
		for range t.C {
			for _, u := range r.ups {
				u.check(r.client)
			}
		}
	}()
	return r
}

// Candidates devuelve los upstreams sanos que sirven el modelo, en orden de intento
// (reparto round-robin ponderado entre los sanos; los no sanos al final como último recurso).
func (r *Router) Candidates(model string) []*Upstream {
	var healthy, sick []*Upstream
	for _, u := range r.ups {
		if !u.Serves(model) {
			continue
		}
		if u.healthy.Load() {
			healthy = append(healthy, u)
		} else {
			sick = append(sick, u)
		}
	}
	if len(healthy) > 1 {
		// Expandir por peso y rotar según un contador compartido.
		var ring []*Upstream
		for _, u := range healthy {
			w := u.Weight
			if w < 1 {
				w = 1
			}
			for i := 0; i < w; i++ {
				ring = append(ring, u)
			}
		}
		start := int(healthy[0].rr.Add(1) % uint64(len(ring))) // #nosec G115 -- acotado por len(ring)
		var ordered []*Upstream
		seen := map[*Upstream]bool{}
		for i := 0; i < len(ring); i++ {
			u := ring[(start+i)%len(ring)]
			if !seen[u] {
				seen[u] = true
				ordered = append(ordered, u)
			}
		}
		healthy = ordered
	}
	return append(healthy, sick...)
}

func (r *Router) Models() []string {
	set := map[string]bool{}
	for _, u := range r.ups {
		for _, m := range u.AllModels() {
			set[m] = true
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func (r *Router) IsCloudModel(model string) bool {
	for _, u := range r.ups {
		if u.Serves(model) && !u.Cloud {
			return false // hay una opción local: no es cloud
		}
	}
	for _, u := range r.ups {
		if u.Serves(model) && u.Cloud {
			return true
		}
	}
	return false
}

type upstreamStatus struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	URL       string   `json:"url"`
	Cloud     bool     `json:"cloud"`
	Healthy   bool     `json:"healthy"`
	LastError string   `json:"last_error,omitempty"`
	LastCheck string   `json:"last_check"`
	Models    []string `json:"models"`
}

func (r *Router) Status() []upstreamStatus {
	var out []upstreamStatus
	for _, u := range r.ups {
		out = append(out, upstreamStatus{Name: u.Name, Type: u.Type, URL: u.URL, Cloud: u.Cloud,
			Healthy: u.healthy.Load(), LastError: u.lastErr.Load().(string),
			LastCheck: time.Unix(u.lastCheck.Load(), 0).UTC().Format(time.RFC3339), Models: u.AllModels()})
	}
	return out
}

// priceFor busca el precio del modelo en el upstream (USD por millón de tokens).
func (u *Upstream) priceFor(model string) (Price, bool) {
	if p, ok := u.Prices[model]; ok {
		return p, true
	}
	// Coincidencia por prefijo para variantes con fecha (gpt-4o-mini-2024-…).
	for m, p := range u.Prices {
		if strings.HasPrefix(model, m) {
			return p, true
		}
	}
	return Price{}, false
}

var _ = yamlmini.Str // el paquete se usa en config.go; evita avisos si se reorganiza
