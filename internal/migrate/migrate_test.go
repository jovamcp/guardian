package migrate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPending(t *testing.T) {
	cases := []struct {
		from, to string
		want     int
	}{
		{"0.2.0", "0.3.0", 1}, {"0.2.0", "0.4.0", 1}, {"0.3.0", "0.4.0", 0},
		{"0.3.0-dev", "0.3.0", 1}, {"0.1.0-beta.1", "0.2.0", 0}, {"0.4.0", "0.3.0", 0},
	}
	for _, c := range cases {
		p, err := Pending(c.from, c.to)
		if err != nil || len(p) != c.want {
			t.Errorf("Pending(%s,%s) = %d, %v; want %d", c.from, c.to, len(p), err, c.want)
		}
	}
	if _, err := Pending("x", "0.3.0"); err == nil {
		t.Error("se esperaba error con versión no válida")
	}
}

func TestRunLiteLLMMigration(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // sin docker: no se listan volúmenes
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "compose", "litellm"), 0o755)
	os.WriteFile(filepath.Join(root, "compose", "litellm", "config.yaml"), []byte("model_list: []\n"), 0o644)
	env := "DOMAIN=ai.home\n# LiteLLM\nLITELLM_MASTER_KEY=sk-1234567890abcdef1234\nLITELLM_SALT_KEY=sk-salt\nLITELLM_DB_PASSWORD=p\nGRAFANA_ADMIN_PASSWORD=g\n"
	os.WriteFile(filepath.Join(root, "compose", ".env"), []byte(env), 0o600)

	var log bytes.Buffer
	applied, err := Run(root, "0.2.0", "0.4.0", &log)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, log.String())
	}
	if strings.Join(applied, ",") != "litellm-to-gateway" {
		t.Errorf("aplicadas: %v", applied)
	}
	got, _ := os.ReadFile(filepath.Join(root, "compose", ".env"))
	want := "DOMAIN=ai.home\n# LiteLLM\nGRAFANA_ADMIN_PASSWORD=g\nGATEWAY_MASTER_KEY=sk-1234567890abcdef1234\n"
	if string(got) != want {
		t.Errorf(".env = %q\nquería %q", got, want)
	}
	if st, _ := os.Stat(filepath.Join(root, "compose", ".env")); st.Mode().Perm() != 0o600 {
		t.Errorf("modo de .env: %o", st.Mode().Perm())
	}
	if dirExists(filepath.Join(root, "compose", "litellm")) {
		t.Error("compose/litellm/ debería haberse eliminado")
	}
	if State(root) != "0.4.0" {
		t.Errorf("estado = %q", State(root))
	}
	// Idempotente: segunda pasada desde 0.2.0 no cambia nada.
	log.Reset()
	if _, err := Run(root, "0.2.0", "0.4.0", &log); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(filepath.Join(root, "compose", ".env"))
	if string(got2) != want {
		t.Errorf("segunda pasada alteró .env: %q", got2)
	}
	// Con GATEWAY_MASTER_KEY ya presente no se pisa.
	os.WriteFile(filepath.Join(root, "compose", ".env"), []byte("GATEWAY_MASTER_KEY=sk-existing-existing-1\nLITELLM_MASTER_KEY=sk-1234567890abcdef1234\n"), 0o600)
	if _, err := Run(root, "0.2.0", "0.3.0", &log); err != nil {
		t.Fatal(err)
	}
	got3, _ := os.ReadFile(filepath.Join(root, "compose", ".env"))
	if string(got3) != "GATEWAY_MASTER_KEY=sk-existing-existing-1\n" {
		t.Errorf(".env con clave existente: %q", got3)
	}
	// Sin .env ni compose/litellm tampoco falla (instalación mínima).
	if _, err := Run(t.TempDir(), "0.2.0", "0.3.0", &log); err != nil {
		t.Errorf("instalación vacía: %v", err)
	}
}

func TestRunNothingPendingStillRecordsState(t *testing.T) {
	root := t.TempDir()
	applied, err := Run(root, "0.3.0", "0.4.0", &bytes.Buffer{})
	if err != nil || len(applied) != 0 || State(root) != "0.4.0" {
		t.Errorf("applied=%v err=%v state=%q", applied, err, State(root))
	}
}
