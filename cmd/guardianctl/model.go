// guardianctl model — verificación y fijación de los modelos de Ollama (docs/prompts/07-v0.4.md).
//
// Ollama guarda cada modelo como un manifiesto JSON (manifests/<registro>/<espacio>/<nombre>/<tag>)
// que enumera blobs por digest (blobs/sha256-<hex>). Guardian:
//   - `model list`:   modelos del almacén con el digest de su manifiesto.
//   - `model verify`: recalcula el SHA-256 de cada blob y lo compara con su nombre (integridad).
//   - `model pin`:    fija en guardian.yaml (`models:`) el digest del manifiesto de cada modelo;
//     `doctor` falla si un modelo fijado cambió (re-pull con otro contenido) y
//     avisa de modelos sin fijar.
//
// Todo es de solo lectura sobre el volumen ollama_data; los resultados se envían a Vector como
// eventos `model_check` para el panel de estado y la alerta.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const ollamaVolume = "guardian_ollama_data"

// modelDigestRe: digest de manifiesto o blob de Ollama (sin "@", a diferencia de digestRe de imágenes).
var modelDigestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type ollamaLayer struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type ollamaManifest struct {
	Config ollamaLayer   `json:"config"`
	Layers []ollamaLayer `json:"layers"`
}

// model es un modelo del almacén de Ollama.
type model struct {
	Name     string // p. ej. qwen2.5:0.5b, o usuario/modelo:tag si no es de library
	Digest   string // sha256:<hex> del archivo de manifiesto
	Manifest ollamaManifest
	Path     string
	Store    string // directorio models/ del que procede
}

// modelProblem es un hallazgo de verify/doctor sobre un modelo.
type modelProblem struct {
	Model  string `json:"model"`
	Kind   string `json:"kind"` // digest_changed | blob_missing | blob_size | blob_mismatch | unpinned | pinned_missing
	Detail string `json:"detail"`
}

// modelCheckEvent es lo que se envía a Vector (event=model_check).
type modelCheckEvent struct {
	Timestamp string         `json:"timestamp"`
	Version   string         `json:"version"`
	Host      string         `json:"host"`
	Mode      string         `json:"mode"` // verify | doctor
	Status    string         `json:"status"`
	Models    int            `json:"models"`
	Blobs     int            `json:"blobs"`
	Bytes     int64          `json:"bytes"`
	Problems  []modelProblem `json:"problems"`
}

func cmdModel(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "uso: guardianctl model list | verify [--report] [<modelo>…] | pin [--all|<modelo>…] | unpin <modelo>…")
		return 2
	}
	fs := flag.NewFlagSet("model", flag.ContinueOnError)
	store := fs.String("store", "", "directorio de modelos de Ollama (por defecto, el volumen "+ollamaVolume+")")
	all := fs.Bool("all", false, "pin: todos los modelos del almacén")
	report := fs.Bool("report", false, "verify: enviar el resultado a Vector (panel de estado)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	root := repoRoot()
	dir := *store
	if dir == "" {
		var err error
		if dir, err = ollamaStore(); err != nil {
			fmt.Fprintln(os.Stderr, "model:", err)
			return 1
		}
	}
	switch args[0] {
	case "list":
		models, err := listModels(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "model list:", err)
			return 1
		}
		c, _ := loadConfig(root)
		for _, m := range models {
			state := "sin fijar"
			if pinned, ok := c.Models[m.Name]; ok {
				if pinned == m.Digest {
					state = "fijado"
				} else {
					state = "CAMBIADO (fijado " + short(pinned) + ")"
				}
			}
			fmt.Printf("%-32s %s  %6.1f GB  %s\n", m.Name, short(m.Digest), float64(m.size())/1e9, state)
		}
		if len(models) == 0 {
			fmt.Println("no hay modelos en", dir)
		}
		return 0
	case "verify":
		models, err := selectModels(dir, fs.Args(), true)
		if err != nil {
			fmt.Fprintln(os.Stderr, "model verify:", err)
			return 1
		}
		ev := verifyModels(models, os.Stdout)
		if *report {
			if err := postModelCheck(ev); err != nil {
				fmt.Fprintln(os.Stderr, "model verify --report:", err)
			}
		}
		if ev.Status != "ok" {
			return 1
		}
		return 0
	case "pin":
		if !*all && len(fs.Args()) == 0 {
			fmt.Fprintln(os.Stderr, "model pin: indica modelos o --all")
			return 2
		}
		models, err := selectModels(dir, fs.Args(), *all)
		if err != nil {
			fmt.Fprintln(os.Stderr, "model pin:", err)
			return 1
		}
		c, err := loadConfig(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "model pin: guardian.yaml:", err)
			return 1
		}
		if c.Models == nil {
			c.Models = map[string]string{}
		}
		for _, m := range models {
			c.Models[m.Name] = m.Digest
			fmt.Printf("fijado %s = %s\n", m.Name, m.Digest)
		}
		if err := saveConfig(root, c); err != nil {
			fmt.Fprintln(os.Stderr, "model pin:", err)
			return 1
		}
		return 0
	case "unpin":
		if len(fs.Args()) == 0 {
			fmt.Fprintln(os.Stderr, "model unpin: indica modelos")
			return 2
		}
		c, err := loadConfig(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "model unpin: guardian.yaml:", err)
			return 1
		}
		for _, name := range fs.Args() {
			if _, ok := c.Models[name]; !ok {
				fmt.Fprintf(os.Stderr, "model unpin: %s no estaba fijado\n", name)
				continue
			}
			delete(c.Models, name)
			fmt.Println("liberado", name)
		}
		if err := saveConfig(root, c); err != nil {
			fmt.Fprintln(os.Stderr, "model unpin:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "model: subcomando desconocido:", args[0])
	return 2
}

func short(d string) string {
	h := strings.TrimPrefix(d, "sha256:")
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func (m model) size() int64 {
	var n int64
	for _, l := range m.Manifest.Layers {
		n += l.Size
	}
	return n + m.Manifest.Config.Size
}

// ollamaStore devuelve el directorio de modelos del volumen de Ollama (requiere root para leerlo).
func ollamaStore() (string, error) {
	out, err := run("docker", "volume", "inspect", ollamaVolume, "--format", "{{.Mountpoint}}")
	if err != nil {
		return "", fmt.Errorf("volumen %s: %v", ollamaVolume, err)
	}
	dir := filepath.Join(strings.TrimSpace(out), "models")
	if _, err := os.Stat(filepath.Join(dir, "manifests")); err != nil {
		return "", fmt.Errorf("%s no contiene manifiestos de Ollama (¿sin modelos aún, o sin permisos: requiere root?)", dir)
	}
	return dir, nil
}

// listModels recorre manifests/<registro>/<espacio>/<nombre>/<tag>.
func listModels(dir string) ([]model, error) {
	base := filepath.Join(dir, "manifests")
	var models []model
	err := filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 4 {
			return nil // no es <registro>/<espacio>/<nombre>/<tag>
		}
		raw, err := os.ReadFile(p) // #nosec G122 -- volumen de Docker propiedad de root (límite de confianza = el host), lectura
		if err != nil {
			return err
		}
		var mf ollamaManifest
		if err := json.Unmarshal(raw, &mf); err != nil {
			return fmt.Errorf("%s: manifiesto ilegible: %v", rel, err)
		}
		name := parts[2] + ":" + parts[3]
		if parts[1] != "library" {
			name = parts[1] + "/" + name
		}
		if parts[0] != "registry.ollama.ai" {
			name = parts[0] + "/" + name
		}
		sum := sha256.Sum256(raw)
		models = append(models, model{Name: name, Digest: "sha256:" + hex.EncodeToString(sum[:]), Manifest: mf, Path: p, Store: dir})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models, nil
}

func selectModels(dir string, names []string, all bool) ([]model, error) {
	models, err := listModels(dir)
	if err != nil {
		return nil, err
	}
	if all || len(names) == 0 {
		return models, nil
	}
	byName := map[string]model{}
	for _, m := range models {
		byName[m.Name] = m
	}
	var out []model
	for _, n := range names {
		if !strings.Contains(n, ":") {
			n += ":latest"
		}
		m, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("modelo %s no está en el almacén (guardianctl model list)", n)
		}
		out = append(out, m)
	}
	return out, nil
}

func blobPath(dir, digest string) string {
	return filepath.Join(dir, "blobs", strings.Replace(digest, ":", "-", 1))
}

// checkBlobs comprueba existencia y tamaño de los blobs de un modelo; con deep, también su SHA-256.
func checkBlobs(dir string, m model, deep bool) (problems []modelProblem, blobs int, bytesRead int64) {
	layers := append([]ollamaLayer{m.Manifest.Config}, m.Manifest.Layers...)
	for _, l := range layers {
		if !modelDigestRe.MatchString(l.Digest) {
			problems = append(problems, modelProblem{m.Name, "blob_mismatch", "digest no válido en el manifiesto: " + l.Digest})
			continue
		}
		p := blobPath(dir, l.Digest)
		st, err := os.Stat(p)
		if err != nil {
			problems = append(problems, modelProblem{m.Name, "blob_missing", short(l.Digest) + " (" + l.MediaType + ")"})
			continue
		}
		blobs++
		if st.Size() != l.Size {
			problems = append(problems, modelProblem{m.Name, "blob_size", fmt.Sprintf("%s: %d bytes, el manifiesto dice %d", short(l.Digest), st.Size(), l.Size)})
			continue
		}
		if !deep {
			continue
		}
		got, err := fileSHA256(p)
		if err != nil {
			problems = append(problems, modelProblem{m.Name, "blob_mismatch", short(l.Digest) + ": " + err.Error()})
			continue
		}
		bytesRead += st.Size()
		if "sha256:"+got != l.Digest {
			problems = append(problems, modelProblem{m.Name, "blob_mismatch", fmt.Sprintf("%s: SHA-256 real %s", short(l.Digest), got[:12])})
		}
	}
	return problems, blobs, bytesRead
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyModels recalcula el SHA-256 de todos los blobs de los modelos dados.
func verifyModels(models []model, out io.Writer) modelCheckEvent {
	host, _ := os.Hostname()
	ev := modelCheckEvent{Timestamp: time.Now().UTC().Format(time.RFC3339), Version: version, Host: host, Mode: "verify", Status: "ok", Models: len(models)}
	start := time.Now()
	for _, m := range models {
		probs, blobs, n := checkBlobs(m.Store, m, true)
		ev.Blobs += blobs
		ev.Bytes += n
		if len(probs) == 0 {
			fmt.Fprintf(out, "[ OK ] %s (%d blobs, %.1f GB)\n", m.Name, blobs, float64(m.size())/1e9)
			continue
		}
		ev.Status = "fail"
		ev.Problems = append(ev.Problems, probs...)
		for _, p := range probs {
			fmt.Fprintf(out, "[FAIL] %s: %s %s\n", m.Name, p.Kind, p.Detail)
		}
	}
	if ev.Problems == nil {
		ev.Problems = []modelProblem{}
	}
	fmt.Fprintf(out, "model verify: %d modelos, %d blobs, %.1f GB leídos en %s → %s\n", ev.Models, ev.Blobs, float64(ev.Bytes)/1e9, time.Since(start).Round(time.Second), ev.Status)
	return ev
}

// doctorModelCheck: comprobación barata para el doctor (cada 15 min): manifiestos fijados sin
// cambios, blobs presentes con su tamaño, y aviso de modelos sin fijar o fijados que ya no están.
func doctorModelCheck(dir string, pinned map[string]string) (modelCheckEvent, error) {
	host, _ := os.Hostname()
	ev := modelCheckEvent{Timestamp: time.Now().UTC().Format(time.RFC3339), Version: version, Host: host, Mode: "doctor", Status: "ok", Problems: []modelProblem{}}
	models, err := listModels(dir)
	if err != nil {
		return ev, err
	}
	ev.Models = len(models)
	seen := map[string]bool{}
	var warns []string
	for _, m := range models {
		seen[m.Name] = true
		if d, ok := pinned[m.Name]; ok && d != m.Digest {
			ev.Problems = append(ev.Problems, modelProblem{m.Name, "digest_changed", "fijado " + short(d) + ", ahora " + short(m.Digest)})
		} else if !ok {
			ev.Problems = append(ev.Problems, modelProblem{m.Name, "unpinned", "sin fijar: guardianctl model pin " + m.Name})
			warns = append(warns, m.Name+" sin fijar")
		}
		probs, blobs, _ := checkBlobs(dir, m, false)
		ev.Blobs += blobs
		ev.Problems = append(ev.Problems, probs...)
	}
	for name := range pinned {
		if !seen[name] {
			ev.Problems = append(ev.Problems, modelProblem{name, "pinned_missing", "fijado en guardian.yaml pero no está en Ollama"})
			warns = append(warns, name+" fijado pero ausente")
		}
	}
	var fails []string
	for _, p := range ev.Problems {
		switch p.Kind {
		case "unpinned", "pinned_missing":
			continue
		}
		fails = append(fails, p.Model+": "+p.Kind+" "+p.Detail)
	}
	if len(fails) > 0 {
		ev.Status = "fail"
		return ev, errors.New(strings.Join(fails, "; "))
	}
	if len(warns) > 0 {
		ev.Status = "warn"
		return ev, errWarn{strings.Join(warns, "; ") + " (guardianctl model pin --all fija los digests actuales)"}
	}
	return ev, nil
}

// checkModels es la comprobación del doctor. Sin volumen de Ollama (runtime externo) avisa.
func checkModels(root string) error {
	c, err := loadConfig(root)
	if err != nil {
		return errWarn{"guardian.yaml: " + err.Error()}
	}
	dir, err := ollamaStore()
	if err != nil {
		if len(c.Models) > 0 {
			return err
		}
		return errWarn{err.Error()}
	}
	ev, err := doctorModelCheck(dir, c.Models)
	if perr := postModelCheck(ev); perr != nil && os.Getenv("GUARDIAN_NO_REPORT") == "" {
		fmt.Fprintln(os.Stderr, "model_check → Vector:", perr)
	}
	return err
}

// postModelCheck envía el evento al http_server de Vector (8689 /model), como el informe del doctor.
func postModelCheck(ev modelCheckEvent) error {
	ip, err := run("docker", "inspect", "gd-vector", "--format", "{{(index .NetworkSettings.Networks \"gd_audit\").IPAddress}}")
	if err != nil {
		return fmt.Errorf("IP de gd-vector: %v", err)
	}
	body, _ := json.Marshal(ev)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("http://"+strings.TrimSpace(ip)+":8689/model", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("vector respondió %d", resp.StatusCode)
	}
	return nil
}
