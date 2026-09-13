// Configuración del gateway (compose/gateway/config.yaml) y variables de entorno.
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jovamcp/guardian/internal/yamlmini"
)

type Price struct{ Input, Output float64 } // USD por millón de tokens

type UpstreamConfig struct {
	Name      string
	Type      string // ollama | openai
	URL       string
	APIKeyEnv string
	Cloud     bool
	Models    []string // nombres o "*" (descubrir en ollama)
	Prices    map[string]Price
	Weight    int
}

type Config struct {
	Listen         string
	DataDir        string
	AuditURL       string
	MasterKey      string
	Upstreams      []UpstreamConfig
	CloudBudgetUSD float64 // presupuesto mensual global para modelos cloud (0 = sin límite)
	DefaultRPM     int
}

func loadConfig(path string) (*Config, error) {
	c := &Config{Listen: ":4000", DataDir: "/data", AuditURL: "http://vector:8687/llm", DefaultRPM: 60}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := yamlmini.Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if v := yamlmini.Str(doc["listen"]); v != "" {
		c.Listen = v
	}
	if v := yamlmini.Str(doc["data_dir"]); v != "" {
		c.DataDir = v
	}
	if v, ok := doc["audit_url"]; ok {
		c.AuditURL = yamlmini.Str(v) // vacío = sin auditoría
	}
	if v := yamlmini.Str(doc["default_rpm"]); v != "" {
		c.DefaultRPM, _ = strconv.Atoi(v)
	}
	if v := yamlmini.Str(yamlmini.Map(doc["budget"])["cloud_monthly_usd"]); v != "" {
		c.CloudBudgetUSD, _ = strconv.ParseFloat(v, 64)
	}
	ups, _ := doc["upstreams"].([]any)
	for _, u := range ups {
		m := yamlmini.Map(u)
		uc := UpstreamConfig{
			Name: yamlmini.Str(m["name"]), Type: yamlmini.Str(m["type"]), URL: strings.TrimRight(yamlmini.Str(m["url"]), "/"),
			APIKeyEnv: yamlmini.Str(m["api_key_env"]), Models: yamlmini.Strs(m["models"]), Prices: map[string]Price{}, Weight: 1,
		}
		if b, ok := m["cloud"].(bool); ok {
			uc.Cloud = b
		}
		if w := yamlmini.Str(m["weight"]); w != "" {
			uc.Weight, _ = strconv.Atoi(w)
		}
		for model, pv := range yamlmini.Map(m["prices"]) {
			pm := yamlmini.Map(pv)
			in, _ := strconv.ParseFloat(yamlmini.Str(pm["input"]), 64)
			out, _ := strconv.ParseFloat(yamlmini.Str(pm["output"]), 64)
			uc.Prices[model] = Price{Input: in, Output: out}
		}
		if uc.Type == "" {
			uc.Type = "ollama"
		}
		c.Upstreams = append(c.Upstreams, uc)
	}
	c.MasterKey = os.Getenv("GATEWAY_MASTER_KEY")
	return c, c.validate()
}

func (c *Config) validate() error {
	if c.MasterKey == "" {
		return errors.New("GATEWAY_MASTER_KEY vacío (compose/.env)")
	}
	if !strings.HasPrefix(c.MasterKey, "sk-") || len(c.MasterKey) < 20 {
		return errors.New("GATEWAY_MASTER_KEY debe empezar por sk- y tener al menos 20 caracteres")
	}
	if len(c.Upstreams) == 0 {
		return errors.New("config: hace falta al menos un upstream")
	}
	seen := map[string]bool{}
	for _, u := range c.Upstreams {
		if u.Name == "" || seen[u.Name] {
			return fmt.Errorf("upstream sin nombre o repetido: %q", u.Name)
		}
		seen[u.Name] = true
		if u.Type != "ollama" && u.Type != "openai" {
			return fmt.Errorf("upstream %s: type debe ser ollama u openai", u.Name)
		}
		if !strings.HasPrefix(u.URL, "http://") && !strings.HasPrefix(u.URL, "https://") {
			return fmt.Errorf("upstream %s: url inválida %q", u.Name, u.URL)
		}
		if u.Type == "openai" && u.APIKeyEnv != "" && os.Getenv(u.APIKeyEnv) == "" {
			return fmt.Errorf("upstream %s: la variable %s está vacía", u.Name, u.APIKeyEnv)
		}
		if u.Cloud && len(u.Prices) == 0 {
			return fmt.Errorf("upstream %s es cloud: declara prices por modelo (USD por millón de tokens)", u.Name)
		}
	}
	return nil
}
