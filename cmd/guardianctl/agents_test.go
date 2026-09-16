package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Todos los manifiestos de ejemplo del repo cargan y validan.
func TestExampleManifestsLoad(t *testing.T) {
	files, _ := filepath.Glob("../../agents/examples/*.yaml")
	if len(files) < 3 {
		t.Fatalf("esperaba al menos 3 manifiestos, hay %d", len(files))
	}
	for _, f := range files {
		m, err := loadManifest(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if len(m.Models) == 0 || m.KeyMode != "auto" {
			t.Errorf("%s: llm.models/key: %v %q", f, m.Models, m.KeyMode)
		}
		for _, mnt := range m.Mounts {
			src, _, _ := strings.Cut(mnt, ":")
			if _, err := os.Stat(filepath.Join(filepath.Dir(f), src)); err != nil {
				t.Errorf("%s: mount %s no existe", f, src)
			}
		}
	}
}

func TestParseTmpfs(t *testing.T) {
	ok := map[string][2]string{"/opt/data": {"/opt/data", "256m"}, "/opt/data:512m": {"/opt/data", "512m"}, "/home/agent/.openclaw:1G": {"/home/agent/.openclaw", "1g"}}
	for in, want := range ok {
		p, s, err := parseTmpfs(in)
		if err != nil || p != want[0] || s != want[1] {
			t.Errorf("%q: %q %q %v", in, p, s, err)
		}
	}
	for _, bad := range []string{"data", "/", "/etc/x", "/run/guardian/secrets", "/usr/lib", "/opt/data:0m", "/opt/data:huge", "/opt/../etc"} {
		if _, _, err := parseTmpfs(bad); err == nil {
			t.Errorf("%q: se esperaba error", bad)
		}
	}
}

func TestBuildRunArgsTmpfsAndEnv(t *testing.T) {
	m, err := loadManifest("../../agents/examples/hermes-agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(buildRunArgs("../..", m, "/x/seccomp.json", "/run/guardian/agents/hermes-agent/secrets"), " ")
	for _, want := range []string{
		"--tmpfs /opt/data:rw,nosuid,nodev,noexec,size=512m,uid=10000,gid=10000,mode=0700",
		"--read-only", "--user 10000:10000", "-e GUARDIAN_MODEL=qwen2.5:0.5b", "-e HERMES_TASK=",
		"--memory 2g", "--pids-limit 256", "bash /opt/guardian-agent/run.sh",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("falta %q en:\n%s", want, args)
		}
	}
	if strings.Contains(args, "llm_key") || strings.Contains(args, "OPENAI_API_KEY=") {
		t.Error("ningún secreto debe ir en la línea de docker run")
	}
}
