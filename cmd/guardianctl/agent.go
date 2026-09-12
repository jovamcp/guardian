// guardianctl agent run: lanza un agente con el perfil de sandbox (DESIGN.md §5, sandbox/README.md).
package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	agentUID       = "10000:10000"
	agentSecretsIn = "/run/guardian/secrets" // dentro del contenedor
	agentRunBase   = "/run/guardian/agents"  // en el host (tmpfs en Debian/Ubuntu)
	apparmorName   = "guardian-agent"
)

func cmdAgent(args []string) int {
	if len(args) < 2 || args[0] != "run" {
		fmt.Fprintln(os.Stderr, "uso: guardianctl agent run <nombre|manifiesto.yaml> [--dry-run]")
		return 2
	}
	root := repoRoot()
	dry := false
	for _, a := range args[2:] {
		if a == "--dry-run" {
			dry = true
		}
	}
	m, err := findManifest(root, args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent run:", err)
		return 1
	}
	if os.Geteuid() != 0 && !dry {
		fmt.Fprintln(os.Stderr, "agent run: requiere root (docker rootful, AppArmor, secretos 0400)")
		return 1
	}
	if err := checkDocker(root); err != nil && !dry {
		fmt.Fprintln(os.Stderr, "agent run:", err)
		return 1
	}
	seccomp := filepath.Join(root, "sandbox", "seccomp-agent.json")
	if _, err := os.Stat(seccomp); err != nil {
		fmt.Fprintln(os.Stderr, "agent run: falta", seccomp)
		return 1
	}
	if !dry {
		if err := ensureAppArmor(root); err != nil {
			fmt.Fprintln(os.Stderr, "agent run:", err)
			return 1
		}
	}

	// Secretos: directorio tmpfs en el host, archivos 0400 propiedad del uid del agente.
	secretsDir := filepath.Join(agentRunBase, m.Name, "secrets")
	var cleanup []func()
	defer func() {
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
	}()
	if !dry {
		if err := os.RemoveAll(filepath.Dir(secretsDir)); err != nil {
			fmt.Fprintln(os.Stderr, "agent run:", err)
			return 1
		}
		if err := os.MkdirAll(secretsDir, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "agent run:", err)
			return 1
		}
		cleanup = append(cleanup, func() { os.RemoveAll(filepath.Dir(secretsDir)) })
	}

	// Llave LLM: efímera (auto) o desde el vault.
	if len(m.Models) > 0 {
		switch {
		case m.KeyMode == "auto":
			if dry {
				fmt.Println("# llm.key: auto → se crearía una llave virtual efímera en LiteLLM")
				break
			}
			alias := fmt.Sprintf("%s-%d", m.Name, time.Now().Unix())
			code, out, err := litellmCall(root, http.MethodPost, "/key/generate", map[string]any{
				"key_alias": alias, "models": m.Models, "duration": "1d",
				"metadata": map[string]any{"guardian_agent": m.Name, "ephemeral": true},
			})
			if err != nil || code != 200 {
				fmt.Fprintf(os.Stderr, "agent run: no se pudo crear la llave LLM (HTTP %d %v %v)\n", code, out, err)
				return 1
			}
			key := ystr(out["key"])
			cleanup = append(cleanup, func() {
				litellmCall(root, http.MethodPost, "/key/delete", map[string]any{"keys": []string{key}})
			})
			if err := writeSecret(secretsDir, "llm_key", key); err != nil {
				fmt.Fprintln(os.Stderr, "agent run:", err)
				return 1
			}
		case strings.HasPrefix(m.KeyMode, "vault:"):
			val, err := vaultGet(root, strings.TrimPrefix(m.KeyMode, "vault:"))
			if err != nil {
				fmt.Fprintln(os.Stderr, "agent run:", err)
				return 1
			}
			if !dry {
				if err := writeSecret(secretsDir, "llm_key", val); err != nil {
					fmt.Fprintln(os.Stderr, "agent run:", err)
					return 1
				}
			}
		}
	}
	for name, sv := range m.Services {
		if sv.Token == "" {
			continue
		}
		val, err := vaultGet(root, strings.TrimPrefix(sv.Token, "vault:"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "agent run:", err)
			return 1
		}
		if !dry {
			if err := writeSecret(secretsDir, name+"_token", val); err != nil {
				fmt.Fprintln(os.Stderr, "agent run:", err)
				return 1
			}
		}
	}

	dockerArgs := buildRunArgs(root, m, seccomp, secretsDir)
	if dry {
		fmt.Println("docker " + shellJoin(dockerArgs))
		return 0
	}

	// Ejecutar en primer plano; Ctrl-C para el contenedor.
	cmd := exec.Command("docker", dockerArgs...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, nil
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		exec.Command("docker", "stop", "-t", "5", "gd-agent-"+m.Name).Run()
	}()
	fmt.Fprintf(os.Stderr, "[guardian] agente %s (%s) → ip %s, modelos %s, egreso %s\n",
		m.Name, m.Image, m.IP, strings.Join(m.Models, ","), strings.Join(m.Allow, ","))
	err = cmd.Run()
	signal.Stop(sig)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			fmt.Fprintf(os.Stderr, "[guardian] agente %s terminó con código %d\n", m.Name, ee.ExitCode())
			return ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "agent run:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "[guardian] agente %s terminó correctamente\n", m.Name)
	return 0
}

func buildRunArgs(root string, m *Manifest, seccomp, secretsDir string) []string {
	args := []string{"run", "--rm", "--init",
		"--name", "gd-agent-" + m.Name, "--hostname", m.Name,
		"--label", "guardian.agent=" + m.Name,
		"--network", "gd_agents", "--ip", m.IP, "--dns", agentsDNS, "--dns-search", ".",
		"--user", agentUID,
		"--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m,uid=10000,gid=10000",
		"--tmpfs", "/home/agent:rw,nosuid,nodev,noexec,size=64m,uid=10000,gid=10000,mode=0700",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--security-opt", "seccomp=" + seccomp,
		"--security-opt", "apparmor=" + apparmorName,
		"--pids-limit", firstNonEmpty(m.PIDs, "128"),
		"--memory", firstNonEmpty(m.Memory, "512m"),
		"--cpus", firstNonEmpty(m.CPUs, "1"),
		"--ulimit", "nofile=1024:1024",
		"-e", "HOME=/home/agent",
		"-e", "OPENAI_BASE_URL=http://litellm:4000/v1",
		"-e", "GUARDIAN_SECRETS_DIR=" + agentSecretsIn,
		"-e", "HTTPS_PROXY=http://" + agentsProxy + ":3128",
		"-e", "HTTP_PROXY=http://" + agentsProxy + ":3128",
		"-e", "https_proxy=http://" + agentsProxy + ":3128",
		"-e", "http_proxy=http://" + agentsProxy + ":3128",
		"-e", "NO_PROXY=litellm," + agentsLLM + ",localhost,127.0.0.1",
		"-e", "no_proxy=litellm," + agentsLLM + ",localhost,127.0.0.1",
	}
	if len(m.Models) > 0 {
		args = append(args, "-e", "GUARDIAN_MODEL="+m.Models[0])
	}
	for k, v := range m.Env {
		args = append(args, "-e", k+"="+v)
	}
	for name, sv := range m.Services {
		if sv.URL != "" {
			args = append(args, "-e", strings.ToUpper(name)+"_URL="+sv.URL)
		}
		if sv.Token != "" {
			args = append(args, "-e", strings.ToUpper(name)+"_TOKEN_FILE="+agentSecretsIn+"/"+name+"_token")
		}
	}
	if secretsDir != "" {
		args = append(args, "--mount", "type=bind,src="+secretsDir+",dst="+agentSecretsIn+",readonly")
	}
	base := filepath.Dir(m.Path)
	for _, mnt := range m.Mounts {
		src, dst, _ := strings.Cut(mnt, ":")
		if !filepath.IsAbs(src) {
			src = filepath.Join(base, src)
		}
		src, _ = filepath.Abs(src)
		args = append(args, "--mount", "type=bind,src="+src+",dst="+dst+",readonly")
	}
	args = append(args, m.Image)
	args = append(args, m.Command...)
	return args
}

func writeSecret(dir, name, value string) error {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(value), 0o400); err != nil {
		return err
	}
	if err := os.Chown(p, 10000, 10000); err != nil {
		return err
	}
	// El directorio debe ser transitable por el uid del agente.
	return os.Chown(dir, 10000, 10000)
}

// ensureAppArmor comprueba que el kernel tiene AppArmor y carga (o recarga) el perfil del repo.
func ensureAppArmor(root string) error {
	if _, err := os.Stat("/sys/kernel/security/apparmor"); err != nil {
		return fmt.Errorf("AppArmor no está activo en este kernel; el sandbox de agentes lo exige en v0.1")
	}
	parser, err := exec.LookPath("apparmor_parser")
	if err != nil {
		return fmt.Errorf("falta apparmor_parser (apt install apparmor)")
	}
	profile := filepath.Join(root, "sandbox", "apparmor", apparmorName)
	if out, err := exec.Command(parser, "-r", profile).CombinedOutput(); err != nil {
		return fmt.Errorf("apparmor_parser -r %s: %v: %s", profile, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func shellJoin(args []string) string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.ContainsAny(a, " \t\"'$") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out = append(out, a)
	}
	return strings.Join(out, " ")
}

// vaultGet se implementa en la tarea 5 (age). Hasta entonces, cualquier referencia vault: falla.
var vaultGet = func(root, ref string) (string, error) {
	return "", fmt.Errorf("vault:%s → el vault llega en la tarea 5 de la Fase 2", ref)
}
