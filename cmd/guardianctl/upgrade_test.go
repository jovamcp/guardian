package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type fakeEntry struct {
	body    string
	mode    int64
	symlink string
	typ     byte
}

// makeTarball construye un guardian-<ver>.tar.gz como el de `make release` (prefijo guardian-<ver>/).
func makeTarball(t *testing.T, ver string, entries map[string]fakeEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	prefix := "guardian-" + ver + "/"
	if err := tw.WriteHeader(&tar.Header{Name: prefix, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e := entries[n]
		name := n
		if !strings.HasPrefix(n, "/") && !strings.HasPrefix(n, "..") {
			name = prefix + n
		}
		hdr := &tar.Header{Name: name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		if e.mode != 0 {
			hdr.Mode = e.mode
		}
		if e.symlink != "" {
			hdr.Typeflag, hdr.Linkname, hdr.Size = tar.TypeSymlink, e.symlink, 0
		}
		if e.typ != 0 {
			hdr.Typeflag = e.typ
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// fakeReleases sirve la API de releases y los artefactos de un release.
type fakeReleases struct {
	srv    *httptest.Server
	tag    string
	assets map[string][]byte
	pre    bool
}

func newFakeReleases(t *testing.T, tag string, tarball []byte, tamperSums bool) *fakeReleases {
	t.Helper()
	ver := strings.TrimPrefix(tag, "v")
	sum := sha256.Sum256(tarball)
	sums := hex.EncodeToString(sum[:]) + "  guardian-" + ver + ".tar.gz\n"
	if tamperSums {
		sums = strings.Repeat("0", 64) + "  guardian-" + ver + ".tar.gz\n"
	}
	f := &fakeReleases{tag: tag, assets: map[string][]byte{
		"guardian-" + ver + ".tar.gz": tarball,
		"SHA256SUMS":                  []byte(sums),
		"SHA256SUMS.sig":              []byte("sig"),
		"SHA256SUMS.pem":              []byte("pem"),
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", f.release)
	mux.HandleFunc("/releases/tags/", f.release)
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/dl/")
		b, ok := f.assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeReleases) release(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/releases/tags/") && !strings.HasSuffix(r.URL.Path, f.tag) {
		http.NotFound(w, r)
		return
	}
	rel := map[string]any{"tag_name": f.tag, "prerelease": f.pre}
	var assets []map[string]string
	for name := range f.assets {
		assets = append(assets, map[string]string{"name": name, "browser_download_url": f.srv.URL + "/dl/" + name})
	}
	rel["assets"] = assets
	json.NewEncoder(w).Encode(rel)
}

// installedTree crea un repo instalado en v0.2.0 con datos de usuario.
func installedTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"VERSION":                     "0.2.0\n",
		"compose/docker-compose.yml":  "services: {old: {}}\n",
		"compose/gateway/config.yaml": "nodes: [mine]\n", // editado por el usuario
		"nftables/guardian.nft":       "define LAN_NET = 10.0.0.0/24\n",
		"old-only.txt":                "solo en 0.2\n",
		"guardian.yaml":               "domain: ai.home\n",
		"compose/.env":                "DOMAIN=ai.home\nWEBUI_SECRET_KEY=abc\n",
		"vault/secret.age":            "cifrado\n",
		"compose/certs/root.crt":      "CA\n",
		"agents/mine.yaml":            "name: mine\n",
		"agents/examples/hello.yaml":  "name: hello (0.2)\n",
		"bin/guardianctl":             "#!/bin/sh\necho 0.2.0\n",
		".env.example":                "DOMAIN=ai.home\nWEBUI_SECRET_KEY=\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func newTarball030(t *testing.T) []byte {
	return makeTarball(t, "0.3.0", map[string]fakeEntry{
		"VERSION":                     {body: "0.3.0\n"},
		"compose/docker-compose.yml":  {body: "services: {new: {}}\n"},
		"compose/gateway/config.yaml": {body: "nodes: [default]\n"},
		"nftables/guardian.nft":       {body: "define LAN_NET = 10.0.0.0/24\n"}, // igual → sin .new
		"new-only.txt":                {body: "solo en 0.3\n"},
		"agents/examples/hello.yaml":  {body: "name: hello (0.3)\n"},
		".env.example":                {body: "DOMAIN=ai.home\nWEBUI_SECRET_KEY=\nGATEWAY_MASTER_KEY=\nGUARDIAN_VERSION=\n"},
		"bin/guardianctl-linux-amd64": {body: "bin", mode: 0o755},
		"bin/guardianctl-linux-arm64": {body: "bin", mode: 0o755},
		"scripts/link.sh":             {symlink: "../install.sh"},
		"install.sh":                  {body: "#!/bin/bash\n", mode: 0o755},
	})
}

type calls struct{ seq []string }

func recordingHooks(c *calls, doctorCode int) upgradeHooks {
	return upgradeHooks{
		backup:  func(string) error { c.seq = append(c.seq, "backup"); return nil },
		install: func(_ string, v string) error { c.seq = append(c.seq, "install:"+v); return nil },
		stack:   func(string) error { c.seq = append(c.seq, "stack"); return nil },
		doctor:  func(string) int { c.seq = append(c.seq, "doctor"); return doctorCode },
		cosign:  func(string) error { c.seq = append(c.seq, "cosign"); return nil },
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestUpgradeHappyPathAndRollback(t *testing.T) {
	root := installedTree(t)
	f := newFakeReleases(t, "v0.3.0", newTarball030(t), false)
	releasesAPI = f.srv.URL + "/releases"
	var c calls
	var out bytes.Buffer
	o := upgradeOpts{root: root, yes: true, out: &out}
	if err := doUpgrade(o, recordingHooks(&c, 0)); err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out.String())
	}
	want := []string{"cosign", "backup", "install:0.3.0", "stack", "doctor"}
	if strings.Join(c.seq, ",") != strings.Join(want, ",") {
		t.Errorf("orden de acciones: %v, quería %v", c.seq, want)
	}
	checks := map[string]string{
		"VERSION":                              "0.3.0\n",
		"compose/docker-compose.yml":           "services: {new: {}}\n",
		"new-only.txt":                         "solo en 0.3\n",
		"old-only.txt":                         "solo en 0.2\n", // no estaba en el tarball: se conserva
		"agents/examples/hello.yaml":           "name: hello (0.3)\n",
		"compose/gateway/config.yaml":          "nodes: [mine]\n", // protegido: intacto
		"compose/gateway/config.yaml.new":      "nodes: [default]\n",
		"guardian.yaml":                        "domain: ai.home\n",
		"compose/.env":                         "DOMAIN=ai.home\nWEBUI_SECRET_KEY=abc\n",
		"vault/secret.age":                     "cifrado\n",
		"compose/certs/root.crt":               "CA\n",
		"agents/mine.yaml":                     "name: mine\n",
		"bin/guardianctl":                      "#!/bin/sh\necho 0.2.0\n", // lo cambia el hook install (aquí simulado)
		".previous/VERSION":                    "0.2.0\n",
		".previous/compose/docker-compose.yml": "services: {old: {}}\n",
	}
	for rel, want := range checks {
		if got := readFile(t, filepath.Join(root, rel)); got != want {
			t.Errorf("%s = %q, quería %q", rel, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "nftables", "guardian.nft.new")); err == nil {
		t.Error("guardian.nft no cambió: no debería haber .new")
	}
	if link, err := os.Readlink(filepath.Join(root, "scripts", "link.sh")); err != nil || link != "../install.sh" {
		t.Errorf("enlace simbólico no extraído: %q %v", link, err)
	}
	if st, err := os.Stat(filepath.Join(root, "install.sh")); err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Errorf("install.sh debería ser ejecutable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, stagingDir)); err == nil {
		t.Error(".upgrade/ debería haberse borrado")
	}
	if !strings.Contains(out.String(), "config.yaml tiene cambios locales") {
		t.Errorf("falta el aviso del archivo protegido:\n%s", out.String())
	}

	// Segunda ejecución: ya al día.
	out.Reset()
	if err := doUpgrade(o, recordingHooks(&c, 0)); err != nil || !strings.Contains(out.String(), "nada que hacer") {
		t.Errorf("segunda ejecución: %v %q", err, out.String())
	}

	// Rollback.
	c = calls{}
	out.Reset()
	if err := doRollback(o, recordingHooks(&c, 0)); err != nil {
		t.Fatalf("rollback: %v\n%s", err, out.String())
	}
	if strings.Join(c.seq, ",") != "install:0.2.0,stack,doctor" {
		t.Errorf("acciones del rollback: %v", c.seq)
	}
	back := map[string]string{
		"VERSION":                         "0.2.0\n",
		"compose/docker-compose.yml":      "services: {old: {}}\n",
		"new-only.txt":                    "<missing>",
		"compose/gateway/config.yaml.new": "<missing>",
		"compose/gateway/config.yaml":     "nodes: [mine]\n",
		"agents/examples/hello.yaml":      "name: hello (0.2)\n",
		"old-only.txt":                    "solo en 0.2\n",
		"guardian.yaml":                   "domain: ai.home\n",
		"vault/secret.age":                "cifrado\n",
	}
	for rel, want := range back {
		if got := readFile(t, filepath.Join(root, rel)); got != want {
			t.Errorf("tras rollback %s = %q, quería %q", rel, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, previousDir)); err == nil {
		t.Error(".previous/ debería haberse borrado tras el rollback")
	}
}

func TestUpgradeRejectsBadChecksumBeforeTouching(t *testing.T) {
	root := installedTree(t)
	f := newFakeReleases(t, "v0.3.0", newTarball030(t), true)
	releasesAPI = f.srv.URL + "/releases"
	var c calls
	err := doUpgrade(upgradeOpts{root: root, yes: true, out: &bytes.Buffer{}}, recordingHooks(&c, 0))
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("se esperaba error de SHA-256, got %v", err)
	}
	if len(c.seq) != 0 {
		t.Errorf("no debería haberse ejecutado nada: %v", c.seq)
	}
	if readFile(t, filepath.Join(root, "VERSION")) != "0.2.0\n" {
		t.Error("VERSION cambió pese al fallo")
	}
	if _, err := os.Stat(filepath.Join(root, previousDir)); err == nil {
		t.Error("no debería existir .previous/")
	}
}

func TestUpgradeRejectsBadSignature(t *testing.T) {
	root := installedTree(t)
	f := newFakeReleases(t, "v0.3.0", newTarball030(t), false)
	releasesAPI = f.srv.URL + "/releases"
	h := recordingHooks(&calls{}, 0)
	h.cosign = func(string) error { return fmt.Errorf("no match") }
	err := doUpgrade(upgradeOpts{root: root, yes: true, out: &bytes.Buffer{}}, h)
	if err == nil || !strings.Contains(err.Error(), "firma cosign") {
		t.Fatalf("se esperaba error de firma, got %v", err)
	}
	if readFile(t, filepath.Join(root, "VERSION")) != "0.2.0\n" {
		t.Error("VERSION cambió pese al fallo")
	}
}

func TestUpgradeRejectsTraversalAndAbsoluteLinks(t *testing.T) {
	bad := []map[string]fakeEntry{
		{"VERSION": {body: "0.3.0\n"}, "../evil": {body: "x"}},
		{"VERSION": {body: "0.3.0\n"}, "/etc/passwd": {body: "x"}},
		{"VERSION": {body: "0.3.0\n"}, "a/b": {symlink: "../../outside"}},
		{"VERSION": {body: "0.3.0\n"}, "a/b": {symlink: "/etc/shadow"}},
		{"VERSION": {body: "0.3.0\n"}, "dev": {typ: tar.TypeChar}},
	}
	for i, entries := range bad {
		root := installedTree(t)
		f := newFakeReleases(t, "v0.3.0", makeTarball(t, "0.3.0", entries), false)
		releasesAPI = f.srv.URL + "/releases"
		var c calls
		err := doUpgrade(upgradeOpts{root: root, yes: true, out: &bytes.Buffer{}}, recordingHooks(&c, 0))
		if err == nil || !strings.Contains(err.Error(), "tarball") {
			t.Errorf("caso %d: se esperaba error del tarball, got %v", i, err)
		}
		if readFile(t, filepath.Join(root, "VERSION")) != "0.2.0\n" {
			t.Errorf("caso %d: VERSION cambió", i)
		}
		for _, s := range c.seq {
			if s != "cosign" {
				t.Errorf("caso %d: se ejecutó %s", i, s)
			}
		}
	}
}

func TestUpgradeVersionPolicy(t *testing.T) {
	root := installedTree(t)
	os.WriteFile(filepath.Join(root, "VERSION"), []byte("0.4.0\n"), 0o644)
	f := newFakeReleases(t, "v0.3.0", newTarball030(t), false)
	releasesAPI = f.srv.URL + "/releases"
	err := doUpgrade(upgradeOpts{root: root, yes: true, out: &bytes.Buffer{}}, recordingHooks(&calls{}, 0))
	if err == nil || !strings.Contains(err.Error(), "--to") {
		t.Errorf("debería negarse a bajar sin --to: %v", err)
	}
	// Con --to explícito sí baja.
	if err := doUpgrade(upgradeOpts{root: root, yes: true, to: "v0.3.0", out: &bytes.Buffer{}}, recordingHooks(&calls{}, 0)); err != nil {
		t.Errorf("con --to debería bajar: %v", err)
	}
	// Prerelease solo con --to.
	root = installedTree(t)
	f.pre = true
	err = doUpgrade(upgradeOpts{root: root, yes: true, out: &bytes.Buffer{}}, recordingHooks(&calls{}, 0))
	if err == nil || !strings.Contains(err.Error(), "prerelease") {
		t.Errorf("prerelease sin --to debería fallar: %v", err)
	}
	// Tarball cuyo VERSION no coincide con el tag.
	root = installedTree(t)
	f = newFakeReleases(t, "v0.3.0", makeTarball(t, "0.3.0", map[string]fakeEntry{"VERSION": {body: "0.9.0\n"}, "compose/docker-compose.yml": {body: "x"}}), false)
	releasesAPI = f.srv.URL + "/releases"
	err = doUpgrade(upgradeOpts{root: root, yes: true, out: &bytes.Buffer{}}, recordingHooks(&calls{}, 0))
	if err == nil || !strings.Contains(err.Error(), "VERSION=0.9.0") {
		t.Errorf("VERSION distinto del tag debería fallar: %v", err)
	}
}

func TestUpgradeBackupFailureAborts(t *testing.T) {
	root := installedTree(t)
	f := newFakeReleases(t, "v0.3.0", newTarball030(t), false)
	releasesAPI = f.srv.URL + "/releases"
	var c calls
	h := recordingHooks(&c, 0)
	h.backup = func(string) error { return fmt.Errorf("restic: repo inaccesible") }
	err := doUpgrade(upgradeOpts{root: root, yes: true, out: &bytes.Buffer{}}, h)
	if err == nil || !strings.Contains(err.Error(), "copia previa") {
		t.Fatalf("se esperaba error de copia previa: %v", err)
	}
	if readFile(t, filepath.Join(root, "VERSION")) != "0.2.0\n" {
		t.Error("VERSION cambió pese a fallar la copia")
	}
	// --no-backup la omite.
	c = calls{}
	if err := doUpgrade(upgradeOpts{root: root, yes: true, noBackup: true, out: &bytes.Buffer{}}, h); err != nil {
		t.Fatalf("--no-backup: %v", err)
	}
	if strings.Join(c.seq, ",") != "cosign,install:0.3.0,stack,doctor" {
		t.Errorf("acciones: %v", c.seq)
	}
}

func TestDoCheck(t *testing.T) {
	root := installedTree(t)
	f := newFakeReleases(t, "v0.3.0", newTarball030(t), false)
	releasesAPI = f.srv.URL + "/releases"
	var out bytes.Buffer
	if err := doCheck(upgradeOpts{root: root, out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "instalada: 0.2.0") || !strings.Contains(out.String(), "hay una versión nueva") {
		t.Errorf("salida inesperada:\n%s", out.String())
	}
	if readFile(t, filepath.Join(root, "VERSION")) != "0.2.0\n" {
		t.Error("--check no debe tocar nada")
	}
}

func TestEnsureEnv(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "compose"), 0o755)
	os.WriteFile(filepath.Join(root, ".env.example"), []byte("# comentario\nDOMAIN=ai.home\nWEBUI_SECRET_KEY=\nGATEWAY_MASTER_KEY=\nGRAFANA_ADMIN_PASSWORD=\nLOKI_RETENTION_PERIOD=720h\nGUARDIAN_VERSION=\n"), 0o644)
	os.WriteFile(filepath.Join(root, "compose", ".env"), []byte("DOMAIN=mi.casa\nWEBUI_SECRET_KEY=ya-existe\nGUARDIAN_VERSION=0.2.0\nLITELLM_MASTER_KEY=sk-old\n"), 0o600)
	if err := ensureEnv(root, "0.3.0"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(root, "compose", ".env"))
	env := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(got), "\n") {
		k, v, _ := strings.Cut(l, "=")
		env[k] = v
	}
	if env["DOMAIN"] != "mi.casa" || env["WEBUI_SECRET_KEY"] != "ya-existe" || env["LITELLM_MASTER_KEY"] != "sk-old" {
		t.Errorf("valores existentes alterados: %q", got)
	}
	if !strings.HasPrefix(env["GATEWAY_MASTER_KEY"], "sk-") || len(env["GATEWAY_MASTER_KEY"]) != 51 {
		t.Errorf("GATEWAY_MASTER_KEY no generada: %q", env["GATEWAY_MASTER_KEY"])
	}
	if len(env["GRAFANA_ADMIN_PASSWORD"]) != 32 || env["LOKI_RETENTION_PERIOD"] != "720h" || env["GUARDIAN_VERSION"] != "0.3.0" {
		t.Errorf("variables nuevas: %q", got)
	}
	if strings.Contains(got, "# comentario") {
		t.Error("los comentarios de .env.example no deben copiarse")
	}
	if st, _ := os.Stat(filepath.Join(root, "compose", ".env")); st.Mode().Perm() != 0o600 {
		t.Errorf("modo de .env: %o", st.Mode().Perm())
	}
}

func TestRandomBase64Length(t *testing.T) {
	if s := randomBase64(32); len(s) != 44 || !strings.HasSuffix(s, "=") {
		t.Errorf("randomBase64(32) = %q (len %d)", s, len(s))
	}
}

func TestUpgradeDryRunTouchesNothing(t *testing.T) {
	root := installedTree(t)
	f := newFakeReleases(t, "v0.3.0", newTarball030(t), false)
	releasesAPI = f.srv.URL + "/releases"
	var c calls
	var out bytes.Buffer
	if err := doUpgrade(upgradeOpts{root: root, dryRun: true, out: &out}, recordingHooks(&c, 0)); err != nil {
		t.Fatalf("dry-run: %v\n%s", err, out.String())
	}
	if strings.Join(c.seq, ",") != "cosign" {
		t.Errorf("dry-run solo debería verificar: %v", c.seq)
	}
	for _, rel := range []string{"VERSION", "compose/docker-compose.yml", "compose/gateway/config.yaml"} {
		if got := readFile(t, filepath.Join(root, rel)); got == "<missing>" || strings.Contains(got, "new") || strings.Contains(got, "0.3.0") {
			t.Errorf("%s cambió en dry-run: %q", rel, got)
		}
	}
	for _, p := range []string{previousDir, stagingDir, "new-only.txt", "compose/gateway/config.yaml.new"} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			t.Errorf("%s no debería existir tras dry-run", p)
		}
	}
	s := out.String()
	if !strings.Contains(s, "protegido: compose/gateway/config.yaml") || !strings.Contains(s, "nuevo: new-only.txt") || !strings.Contains(s, "modificado: compose/docker-compose.yml") {
		t.Errorf("informe del ensayo incompleto:\n%s", s)
	}
}

func TestMigrationsFromResolution(t *testing.T) {
	root := t.TempDir()
	if got := migrationsFrom(root); got != "" {
		t.Errorf("sin registro ni .previous: %q", got)
	}
	os.MkdirAll(filepath.Join(root, previousDir), 0o755)
	os.WriteFile(filepath.Join(root, previousDir, "META"), []byte("version=0.2.0\ngit=abc\n"), 0o600)
	if got := migrationsFrom(root); got != "0.2.0" {
		t.Errorf("desde .previous/META: %q", got)
	}
	os.MkdirAll(filepath.Join(root, "compose"), 0o755)
	os.WriteFile(filepath.Join(root, "compose", ".migrated"), []byte("0.3.0\n"), 0o644)
	if got := migrationsFrom(root); got != "0.3.0" {
		t.Errorf("el registro manda: %q", got)
	}
}

func TestRunMigrationsFromPreviousAndDoctorCheck(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "compose", "litellm"), 0o755)
	os.WriteFile(filepath.Join(root, "compose", ".env"), []byte("LITELLM_MASTER_KEY=sk-master-0123456789abcdef\n"), 0o600)
	os.WriteFile(filepath.Join(root, "VERSION"), []byte("0.4.0\n"), 0o644)
	os.MkdirAll(filepath.Join(root, previousDir), 0o755)
	os.WriteFile(filepath.Join(root, previousDir, "META"), []byte("version=0.2.0\n"), 0o600)
	if err := checkMigrations(root); err == nil || !errors.As(err, new(errWarn)) {
		t.Errorf("sin registro debería avisar: %v", err)
	}
	if err := runMigrations(root, "0.4.0"); err != nil {
		t.Fatal(err)
	}
	env := readFile(t, filepath.Join(root, "compose", ".env"))
	if env != "GATEWAY_MASTER_KEY=sk-master-0123456789abcdef\n" {
		t.Errorf(".env tras migrar: %q", env)
	}
	if readFile(t, filepath.Join(root, "compose", ".migrated")) != "0.4.0\n" {
		t.Error("registro no actualizado")
	}
	if err := checkMigrations(root); err != nil {
		t.Errorf("al día: %v", err)
	}
	// VERSION avanza a una versión con migración pendiente → fallo (no aviso).
	os.WriteFile(filepath.Join(root, "compose", ".migrated"), []byte("0.2.0\n"), 0o644)
	if err := checkMigrations(root); err == nil || errors.As(err, new(errWarn)) {
		t.Errorf("pendientes debería fallar: %v", err)
	}
	// Instalación nueva: sin registro ni .previous → runMigrations solo registra.
	fresh := t.TempDir()
	if err := runMigrations(fresh, "0.4.0"); err != nil || readFile(t, filepath.Join(fresh, "compose", ".migrated")) != "0.4.0\n" {
		t.Errorf("instalación nueva: %v", err)
	}
}
