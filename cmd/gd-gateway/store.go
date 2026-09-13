// Almacén de llaves y uso en archivos JSON con escritura atómica (sin base de datos).
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Key struct {
	ID        string            `json:"id"`
	Alias     string            `json:"alias"`
	Hash      string            `json:"hash"` // sha256 hex de la llave; el valor nunca se guarda
	Prefix    string            `json:"prefix"`
	Models    []string          `json:"models"`
	RPM       int               `json:"rpm"`
	BudgetUSD float64           `json:"budget_usd"` // mensual, solo cuenta el gasto cloud (0 = sin límite)
	Cloud     bool              `json:"cloud"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at,omitempty"`
	Revoked   bool              `json:"revoked"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type KeyUsage struct {
	Requests         int     `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	USD              float64 `json:"usd"`
	Errors           int     `json:"errors"`
}

type Usage struct {
	Month    string               `json:"month"` // YYYY-MM
	Keys     map[string]*KeyUsage `json:"keys"`
	CloudUSD float64              `json:"cloud_usd"`
	Warned80 bool                 `json:"warned_80"`
}

type Store struct {
	mu    sync.Mutex
	dir   string
	keys  []*Key
	usage *Usage
}

func openStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, usage: &Usage{Month: monthNow(), Keys: map[string]*KeyUsage{}}}
	if raw, err := os.ReadFile(filepath.Join(dir, "keys.json")); err == nil {
		if err := json.Unmarshal(raw, &s.keys); err != nil {
			return nil, err
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "usage.json")); err == nil {
		var u Usage
		if err := json.Unmarshal(raw, &u); err == nil && u.Month == monthNow() {
			if u.Keys == nil {
				u.Keys = map[string]*KeyUsage{}
			}
			s.usage = &u
		}
	}
	return s, nil
}

func monthNow() string { return time.Now().UTC().Format("2006-01") }

func writeAtomic(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func hashKey(k string) string {
	h := sha256.Sum256([]byte(k))
	return hex.EncodeToString(h[:])
}

func newSecret() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-gd-" + hex.EncodeToString(b), nil
}

// CreateKey devuelve la llave en claro una sola vez.
func (s *Store) CreateKey(alias string, models []string, rpm int, budget float64, cloud bool, ttl time.Duration, meta map[string]string) (*Key, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.Alias == alias && !k.Revoked {
			return nil, "", errors.New("ya existe una llave activa con ese alias")
		}
	}
	secret, err := newSecret()
	if err != nil {
		return nil, "", err
	}
	idb := make([]byte, 6)
	rand.Read(idb)
	k := &Key{ID: hex.EncodeToString(idb), Alias: alias, Hash: hashKey(secret), Prefix: secret[:12],
		Models: models, RPM: rpm, BudgetUSD: budget, Cloud: cloud, CreatedAt: time.Now().UTC(), Metadata: meta}
	if ttl > 0 {
		k.ExpiresAt = k.CreatedAt.Add(ttl)
	}
	s.keys = append(s.keys, k)
	return k, secret, writeAtomic(filepath.Join(s.dir, "keys.json"), s.keys)
}

func (s *Store) Lookup(secret string) *Key {
	h := hashKey(secret)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.Hash == h && !k.Revoked && (k.ExpiresAt.IsZero() || time.Now().Before(k.ExpiresAt)) {
			return k
		}
	}
	return nil
}

func (s *Store) ListKeys() []*Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Key, 0, len(s.keys))
	for _, k := range s.keys {
		if !k.Revoked {
			out = append(out, k)
		}
	}
	return out
}

func (s *Store) Revoke(idOrAlias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, k := range s.keys {
		if (k.ID == idOrAlias || k.Alias == idOrAlias || strings.HasPrefix(idOrAlias, k.Prefix)) && !k.Revoked {
			k.Revoked = true
			found = true
		}
	}
	if !found {
		return errors.New("llave no encontrada")
	}
	return writeAtomic(filepath.Join(s.dir, "keys.json"), s.keys)
}

func (s *Store) rollMonth() {
	if s.usage.Month != monthNow() {
		s.usage = &Usage{Month: monthNow(), Keys: map[string]*KeyUsage{}}
	}
}

// Record acumula uso y gasto; devuelve el gasto cloud global tras la operación.
func (s *Store) Record(keyID string, promptTok, complTok int64, usd float64, cloud bool, failed bool) (float64, *KeyUsage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollMonth()
	ku := s.usage.Keys[keyID]
	if ku == nil {
		ku = &KeyUsage{}
		s.usage.Keys[keyID] = ku
	}
	ku.Requests++
	ku.PromptTokens += promptTok
	ku.CompletionTokens += complTok
	if failed {
		ku.Errors++
	}
	if cloud {
		ku.USD += usd
		s.usage.CloudUSD += usd
	}
	writeAtomic(filepath.Join(s.dir, "usage.json"), s.usage)
	cp := *ku
	return s.usage.CloudUSD, &cp
}

func (s *Store) UsageSnapshot() Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollMonth()
	cp := Usage{Month: s.usage.Month, CloudUSD: s.usage.CloudUSD, Warned80: s.usage.Warned80, Keys: map[string]*KeyUsage{}}
	for k, v := range s.usage.Keys {
		vv := *v
		cp.Keys[k] = &vv
	}
	return cp
}

func (s *Store) MarkWarned() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage.Warned80 = true
	writeAtomic(filepath.Join(s.dir, "usage.json"), s.usage)
}
