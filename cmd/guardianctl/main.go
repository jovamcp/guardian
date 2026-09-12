// guardianctl — herramienta de línea de comandos de Guardian.
// Solo biblioteca estándar. Subcomandos disponibles en la Fase 1: status, doctor, version.
package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
	case "init":
		os.Exit(cmdInit())
	case "policy", "agent", "key", "secret":
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

  init      exporta la CA interna de Caddy a compose/certs/root.crt e imprime los siguientes pasos
  status    estado de los servicios (docker compose ps)
  doctor    comprobaciones de salud y seguridad (requiere root)
  version   versión
  policy | agent | key | secret   pendientes`)
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
		{"Ollama responde desde gd_ai (open-webui → http://ollama:11434/api/tags)", checkOllamaFromWebUI},
		{"443 presenta un certificado emitido por la CA interna", checkTLSIssuedByInternalCA},
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

// envValue lee una variable de compose/.env (formato KEY=VALUE, sin comillas).
func envValue(root, key string) string {
	f, err := os.Open(filepath.Join(root, "compose", ".env"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, key+"=") {
			return strings.Trim(strings.TrimPrefix(line, key+"="), `"'`)
		}
	}
	return ""
}

func checkOllamaFromWebUI(root string) error {
	out, err := run("docker", composeArgs(root, "exec", "-T", "open-webui",
		"curl", "-sf", "-m", "5", "http://ollama:11434/api/tags")...)
	if err != nil {
		return fmt.Errorf("open-webui no alcanza a ollama: %v", err)
	}
	if !strings.Contains(out, `"models"`) {
		return fmt.Errorf("respuesta inesperada de ollama: %.80s", out)
	}
	return nil
}

// checkTLSIssuedByInternalCA conecta a 127.0.0.1:443 con SNI=DOMAIN y verifica la cadena
// contra compose/certs/root.crt (la CA interna de Caddy).
func checkTLSIssuedByInternalCA(root string) error {
	domain := envValue(root, "DOMAIN")
	if domain == "" {
		return errors.New("DOMAIN no definido en compose/.env")
	}
	pem, err := os.ReadFile(filepath.Join(root, "compose", "certs", "root.crt"))
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return errors.New("compose/certs/root.crt no contiene un certificado PEM válido")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", "127.0.0.1:443", &tls.Config{
		ServerName: domain,
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return fmt.Errorf("handshake TLS con SNI %s falló: %v", domain, err)
	}
	defer conn.Close()
	leaf := conn.ConnectionState().PeerCertificates[0]
	if err := leaf.VerifyHostname(domain); err != nil {
		return err
	}
	return nil
}

// cmdInit exporta la CA interna de Caddy a compose/certs/root.crt e imprime los siguientes pasos.
func cmdInit() int {
	root := repoRoot()
	certDir := filepath.Join(root, "compose", "certs")
	dst := filepath.Join(certDir, "root.crt")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		return 1
	}
	if _, err := run("docker", composeArgs(root, "up", "-d", "caddy")...); err != nil {
		fmt.Fprintln(os.Stderr, "init: no se pudo levantar caddy:", err)
		return 1
	}
	const src = "gd-caddy:/data/caddy/pki/authorities/local/root.crt"
	var lastErr error
	for i := 0; i < 30; i++ {
		if _, lastErr = run("docker", "cp", src, dst); lastErr == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		fmt.Fprintln(os.Stderr, "init: caddy no generó la CA:", lastErr)
		return 1
	}
	_ = os.Chmod(dst, 0o644)
	fmt.Println("CA interna exportada a", dst)
	printNextSteps(root)
	return 0
}

func printNextSteps(root string) {
	domain := envValue(root, "DOMAIN")
	if domain == "" {
		domain = "<DOMAIN>"
	}
	wgHost := envValue(root, "WG_HOST")
	if wgHost == "" {
		wgHost = "<WG_HOST>"
	}
	fmt.Printf(`
Siguientes pasos:
  1. DNS local: %s, id.%s y vpn.%s → IP de este host.
  2. Instala la CA compose/certs/root.crt en tus dispositivos (docs/instalacion.md).
  3. Pocket ID: https://id.%s/setup → admin con passkey y cliente OIDC "open-webui"
     con callback https://%s/oauth/oidc/callback
  4. Copia OAUTH_CLIENT_ID / OAUTH_CLIENT_SECRET a compose/.env y ejecuta: make restart
  5. WireGuard: https://vpn.%s → asistente (host %s, puerto 51820) → QR para el móvil.
  6. Comprueba: make doctor
`, domain, domain, domain, domain, domain, domain, wgHost)
}
