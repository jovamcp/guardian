package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeStore crea un almacén de Ollama con un modelo library/qwen2.5:0.5b y otro usuario/mio:latest.
func fakeStore(t *testing.T) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "blobs"), 0o755)
	digests := map[string]string{}
	put := func(name, body string) ollamaLayer {
		sum := sha256.Sum256([]byte(body))
		d := "sha256:" + hex.EncodeToString(sum[:])
		os.WriteFile(filepath.Join(dir, "blobs", strings.Replace(d, ":", "-", 1)), []byte(body), 0o644)
		return ollamaLayer{MediaType: "application/vnd.ollama.image." + name, Digest: d, Size: int64(len(body))}
	}
	write := func(reg, ns, name, tag string, mf ollamaManifest) {
		p := filepath.Join(dir, "manifests", reg, ns, name, tag)
		os.MkdirAll(filepath.Dir(p), 0o755)
		raw, _ := json.Marshal(mf)
		os.WriteFile(p, raw, 0o644)
		sum := sha256.Sum256(raw)
		key := name + ":" + tag
		if ns != "library" {
			key = ns + "/" + key
		}
		digests[key] = "sha256:" + hex.EncodeToString(sum[:])
	}
	write("registry.ollama.ai", "library", "qwen2.5", "0.5b", ollamaManifest{
		Config: put("config", `{"arch":"qwen2"}`),
		Layers: []ollamaLayer{put("model", strings.Repeat("GGUF-qwen", 1000)), put("template", "{{ .Prompt }}")},
	})
	write("registry.ollama.ai", "usuario", "mio", "latest", ollamaManifest{
		Config: put("config", `{"arch":"x"}`),
		Layers: []ollamaLayer{put("model", strings.Repeat("GGUF-mio", 500))},
	})
	return dir, digests
}

func TestListAndVerifyModels(t *testing.T) {
	dir, digests := fakeStore(t)
	models, err := listModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Name != "qwen2.5:0.5b" || models[1].Name != "usuario/mio:latest" {
		t.Fatalf("modelos: %+v", models)
	}
	if models[0].Digest != digests["qwen2.5:0.5b"] || models[0].Store != dir {
		t.Errorf("digest/store: %s %s", models[0].Digest, models[0].Store)
	}
	var out bytes.Buffer
	ev := verifyModels(models, &out)
	if ev.Status != "ok" || ev.Blobs != 5 || len(ev.Problems) != 0 {
		t.Errorf("verify limpio: %+v\n%s", ev, out.String())
	}
	// Alterar un byte del blob del modelo qwen → blob_mismatch (mismo tamaño).
	p := blobPath(dir, models[0].Manifest.Layers[0].Digest)
	raw, _ := os.ReadFile(p)
	raw[10] ^= 0xff
	os.WriteFile(p, raw, 0o644)
	// Borrar el template de mio → blob_missing.
	os.Remove(blobPath(dir, models[1].Manifest.Config.Digest))
	out.Reset()
	ev = verifyModels(models, &out)
	if ev.Status != "fail" || len(ev.Problems) != 2 {
		t.Fatalf("verify con fallos: %+v\n%s", ev, out.String())
	}
	kinds := ev.Problems[0].Kind + "," + ev.Problems[1].Kind
	if kinds != "blob_mismatch,blob_missing" {
		t.Errorf("tipos: %s", kinds)
	}
	// Truncar → blob_size (sin hash).
	os.WriteFile(p, raw[:100], 0o644)
	probs, _, _ := checkBlobs(dir, models[0], false)
	if len(probs) != 1 || probs[0].Kind != "blob_size" {
		t.Errorf("blob_size: %+v", probs)
	}
	// selectModels: nombre sin tag → :latest; desconocido → error.
	if sel, err := selectModels(dir, []string{"usuario/mio"}, false); err != nil || len(sel) != 1 {
		t.Errorf("select mio: %v %d", err, len(sel))
	}
	if _, err := selectModels(dir, []string{"nada:1b"}, false); err == nil {
		t.Error("select desconocido debería fallar")
	}
}

func TestDoctorModelCheck(t *testing.T) {
	dir, digests := fakeStore(t)
	// Sin nada fijado: aviso por modelos sin fijar.
	ev, err := doctorModelCheck(dir, nil)
	if err == nil || !errors.As(err, new(errWarn)) || ev.Status != "warn" {
		t.Errorf("sin fijar: %v %s", err, ev.Status)
	}
	// Todo fijado y sin cambios: OK.
	ev, err = doctorModelCheck(dir, digests)
	if err != nil || ev.Status != "ok" || ev.Models != 2 || ev.Blobs != 5 {
		t.Errorf("fijado: %v %+v", err, ev)
	}
	// Re-pull con otro contenido: el manifiesto cambia → fallo digest_changed.
	pinned := map[string]string{}
	for k, v := range digests {
		pinned[k] = v
	}
	pinned["qwen2.5:0.5b"] = "sha256:" + strings.Repeat("0", 64)
	ev, err = doctorModelCheck(dir, pinned)
	if err == nil || errors.As(err, new(errWarn)) || ev.Status != "fail" || !strings.Contains(err.Error(), "digest_changed") {
		t.Errorf("cambiado: %v %s", err, ev.Status)
	}
	// Fijado pero ausente → aviso.
	pinned = map[string]string{"qwen2.5:0.5b": digests["qwen2.5:0.5b"], "usuario/mio:latest": digests["usuario/mio:latest"], "llama3:8b": "sha256:" + strings.Repeat("a", 64)}
	ev, err = doctorModelCheck(dir, pinned)
	if err == nil || !errors.As(err, new(errWarn)) || !strings.Contains(err.Error(), "llama3:8b") {
		t.Errorf("ausente: %v", err)
	}
	// Blob que falta en un modelo fijado → fallo.
	models, _ := listModels(dir)
	os.Remove(blobPath(dir, models[1].Manifest.Layers[0].Digest))
	_, err = doctorModelCheck(dir, digests)
	if err == nil || errors.As(err, new(errWarn)) || !strings.Contains(err.Error(), "blob_missing") {
		t.Errorf("blob ausente: %v", err)
	}
}

func TestConfigModelsRoundTrip(t *testing.T) {
	root := t.TempDir()
	c := defaultConfig()
	c.Models = map[string]string{"qwen2.5:0.5b": "sha256:" + strings.Repeat("ab", 32), "usuario/mio:latest": "sha256:" + strings.Repeat("cd", 32)}
	if err := saveConfig(root, c); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(configPath(root))
	if !strings.Contains(string(raw), "\n  \"qwen2.5:0.5b\": sha256:abab") {
		t.Errorf("render:\n%s", raw)
	}
	got, err := loadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 2 || got.Models["qwen2.5:0.5b"] != c.Models["qwen2.5:0.5b"] || got.Models["usuario/mio:latest"] != c.Models["usuario/mio:latest"] {
		t.Errorf("models tras cargar: %v", got.Models)
	}
	// Sin modelos: `models: {}` y mapa vacío.
	c.Models = nil
	saveConfig(root, c)
	raw, _ = os.ReadFile(configPath(root))
	if !strings.Contains(string(raw), "models: {}") {
		t.Errorf("render vacío:\n%s", raw)
	}
	if got, err := loadConfig(root); err != nil || len(got.Models) != 0 {
		t.Errorf("vacío: %v %v", err, got.Models)
	}
	// Digest inválido → error de validación.
	os.WriteFile(configPath(root), []byte(strings.Replace(string(raw), "models: {}", "models:\n  \"x:1\": sha256:corto\n", 1)), 0o644)
	if _, err := loadConfig(root); err == nil || !strings.Contains(err.Error(), "models.") {
		t.Errorf("digest inválido: %v", err)
	}
	// El ejemplo del repo sigue cargando.
	os.WriteFile(configPath(root), mustRead(t, "../../guardian.yaml.example"), 0o644)
	if _, err := loadConfig(root); err != nil {
		t.Errorf("guardian.yaml.example: %v", err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
