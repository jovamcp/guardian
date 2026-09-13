// gd-gateway: gateway compatible con OpenAI para Guardian (Go, solo biblioteca estándar).
// Autentica llaves virtuales, filtra modelos, limita, contabiliza, audita y enruta a uno o
// varios nodos (Ollama u otros servicios compatibles con OpenAI, incluidos los cloud).
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

var version = "dev"

func main() {
	cfgPath := flag.String("config", "/etc/gateway/config.yaml", "archivo de configuración")
	health := flag.Bool("healthcheck", false, "comprueba /health en el propio proceso (para el healthcheck de Docker; la imagen no tiene shell)")
	flag.Parse()
	if *health {
		os.Exit(selfCheck())
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	store, err := openStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	g := newGateway(cfg, store)
	srv := &http.Server{Addr: cfg.Listen, Handler: g.mux(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("gd-gateway %s escuchando en %s, %d upstream(s), auditoría → %s", version, cfg.Listen, len(cfg.Upstreams), orNone(cfg.AuditURL))
	log.Fatal(srv.ListenAndServe())
}

// selfCheck hace GET a /health (exige al menos un upstream sano) en el puerto local.
func selfCheck() int {
	port := os.Getenv("GATEWAY_PORT")
	if port == "" {
		port = "4000"
	}
	c := &http.Client{Timeout: 4 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/health")
	if err != nil || resp.StatusCode != 200 {
		return 1
	}
	resp.Body.Close()
	return 0
}

func orNone(s string) string {
	if s == "" {
		return "(desactivada)"
	}
	return s
}

func newGateway(cfg *Config, store *Store) *Gateway {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ResponseHeaderTimeout: 10 * time.Minute, // los modelos locales en CPU tardan
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
	}
	return &Gateway{cfg: cfg, store: store, router: newRouter(newUpstreams(cfg.Upstreams)),
		audit: newAuditor(cfg.AuditURL), client: &http.Client{Transport: transport},
		rl: &rateLimiter{hits: map[string][]time.Time{}}}
}

func (g *Gateway) mux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/health", g.handleHealth)
	m.HandleFunc("/health/liveliness", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	m.HandleFunc("/v1/models", g.handleModels)
	m.HandleFunc("/v1/chat/completions", g.postOnly(g.handleProxy))
	m.HandleFunc("/v1/completions", g.postOnly(g.handleProxy))
	m.HandleFunc("/v1/embeddings", g.postOnly(g.handleProxy))
	m.HandleFunc("/admin/keys", g.handleAdminKeys)
	m.HandleFunc("/admin/keys/", g.handleAdminKeys)
	m.HandleFunc("/admin/health", g.handleAdminHealth)
	m.HandleFunc("/admin/usage", g.handleAdminUsage)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, 404, "not_found", "ruta no soportada; usa /v1/models, /v1/chat/completions, /v1/completions, /v1/embeddings")
	})
	return logRequests(m)
}

func (g *Gateway) postOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method_not_allowed", "usa POST")
			return
		}
		h(w, r)
	}
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, code: 200}
		start := time.Now()
		h.ServeHTTP(sw, r)
		// Una línea por petición, sin cuerpos: {"ts":…,"method":…,"path":…,"status":…,"ms":…}
		log.Printf(`{"method":%q,"path":%q,"status":%d,"ms":%d,"ip":%q}`, r.Method, r.URL.Path, sw.code, time.Since(start).Milliseconds(), clientIP(r))
	})
}

func init() {
	log.SetFlags(0)
	log.SetOutput(os.Stdout)
}
