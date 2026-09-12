// guardianctl — herramienta de línea de comandos de Guardian.
// Solo biblioteca estándar. Subcomandos disponibles en la Fase 1: status, doctor, version.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const version = "0.1.0-dev"

// Servicios que deben estar en ejecución (nombres de servicio del compose).
var requiredServices = []string{"caddy", "pocket-id", "open-webui", "ollama", "wg-easy"}

// Puertos publicados permitidos (regla dura 2).
var allowedPublished = map[string]bool{"443/tcp": true, "51820/udp": true}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "status":
		os.Exit(cmdStatus())
	case "doctor":
		os.Exit(cmdDoctor())
	case "version":
		fmt.Println("guardianctl", version)
	case "init", "policy", "agent", "key", "secret":
		fmt.Fprintf(os.Stderr, "guardianctl %s: pendiente (se implementa en fases posteriores)\n", os.Args[1])
		os.Exit(1)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "subcomando desconocido: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `uso: guardianctl <subcomando>

  status    estado de los servicios (docker compose ps)
  doctor    comprobaciones de salud y seguridad (requiere root)
  version   versión
  init | policy | agent | key | secret   pendientes`)
}

// repoRoot localiza la raíz del repo: directorio actual o el del binario (bin/..).
func repoRoot() string {
	candidates := []string{"."}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".."))
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "compose", "docker-compose.yml")); err == nil {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return "."
}

func composeArgs(root string, extra ...string) []string {
	args := []string{"compose", "--env-file", filepath.Join(root, "compose", ".env"),
		"-f", filepath.Join(root, "compose", "docker-compose.yml")}
	return append(args, extra...)
}

func run(name string, args ...string) (string, error) {
	var out, stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

func cmdStatus() int {
	root := repoRoot()
	cmd := exec.Command("docker", composeArgs(root, "ps")...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		return 1
	}
	return 0
}

// check es una comprobación del doctor.
type check struct {
	name string
	fn   func(root string) error
}

func cmdDoctor() int {
	root := repoRoot()
	checks := []check{
		{"docker disponible", checkDocker},
		{"los 5 servicios en ejecución", checkServicesRunning},
		{"ningún socket del host escucha en 11434", checkNoHostOllama},
		{"contenedores gd-* solo publican 443/tcp y 51820/udp", checkPublishedPorts},
		{"existe compose/certs/root.crt", checkRootCert},
	}
	failed := 0
	for _, c := range checks {
		if err := c.fn(root); err != nil {
			failed++
			fmt.Printf("[FAIL] %s\n       %v\n", c.name, err)
		} else {
			fmt.Printf("[ OK ] %s\n", c.name)
		}
	}
	if failed > 0 {
		fmt.Printf("\ndoctor: %d comprobación(es) fallida(s)\n", failed)
		return 1
	}
	fmt.Println("\ndoctor: todo correcto")
	return 0
}

func checkDocker(string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("docker no está en PATH")
	}
	if _, err := run("docker", "info"); err != nil {
		return fmt.Errorf("el daemon no responde: %v", err)
	}
	return nil
}

func checkServicesRunning(root string) error {
	out, err := run("docker", composeArgs(root, "ps", "--services", "--filter", "status=running")...)
	if err != nil {
		return err
	}
	running := map[string]bool{}
	for _, s := range strings.Fields(out) {
		running[s] = true
	}
	var missing []string
	for _, s := range requiredServices {
		if !running[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("no están en ejecución: %s", strings.Join(missing, ", "))
	}
	return nil
}

func checkNoHostOllama(string) error {
	out, err := run("ss", "-Hltnu")
	if err != nil {
		return fmt.Errorf("no se pudo ejecutar ss: %v", err)
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// Formato: Netid State Recv-Q Send-Q Local:Port Peer:Port
		if len(f) >= 5 && strings.HasSuffix(f[4], ":11434") {
			return fmt.Errorf("hay un socket escuchando en %s (Ollama debe vivir solo en gd_ai)", f[4])
		}
	}
	return nil
}

func checkPublishedPorts(string) error {
	out, err := run("docker", "ps", "--format", "{{.Names}} {{.Ports}}")
	if err != nil {
		return err
	}
	var bad []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		name, ports, _ := strings.Cut(line, " ")
		if !strings.HasPrefix(name, "gd-") {
			continue
		}
		for _, p := range strings.Split(ports, ",") {
			p = strings.TrimSpace(p)
			// Solo cuentan las entradas con "->" (publicadas en el host).
			if !strings.Contains(p, "->") {
				continue
			}
			_, target, _ := strings.Cut(p, "->")
			if !allowedPublished[target] {
				bad = append(bad, name+": "+p)
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("puertos publicados no permitidos: %s", strings.Join(bad, "; "))
	}
	return nil
}

func checkRootCert(root string) error {
	p := filepath.Join(root, "compose", "certs", "root.crt")
	st, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("%s no existe (ejecuta install.sh)", p)
	}
	if st.Size() == 0 {
		return fmt.Errorf("%s está vacío", p)
	}
	return nil
}
