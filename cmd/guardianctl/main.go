// guardianctl — herramienta de línea de comandos de Guardian.
// Solo biblioteca estándar. Subcomandos disponibles en la Fase 1: status, doctor, version.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// version se fija en compilación: -ldflags "-X main.version=…" (Makefile lee VERSION).
var version = "dev"

// Servicios que deben estar en ejecución (nombres de servicio del compose).
var requiredServices = []string{"caddy", "pocket-id", "open-webui", "ollama", "wg-easy",
	"litellm-db", "litellm", "blocky", "squid",
	"docker-socket-proxy", "vector", "loki", "grafana"}

// errWarn marca una comprobación que no debe hacer fallar al doctor (aviso).
type errWarn struct{ msg string }

func (e errWarn) Error() string { return e.msg }

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
		os.Exit(cmdDoctor(os.Args[2:]))
	case "version":
		fmt.Println("guardianctl", version)
	case "init":
		os.Exit(cmdInit(os.Args[2:]))
	case "key":
		os.Exit(cmdKey(os.Args[2:]))
	case "policy":
		os.Exit(cmdPolicy(os.Args[2:]))
	case "agent":
		os.Exit(cmdAgent(os.Args[2:]))
	case "secret":
		os.Exit(cmdSecret(os.Args[2:]))
	case "backup":
		os.Exit(cmdBackup(os.Args[2:]))
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

  init      crea guardian.yaml (preguntas o flags: --domain --lan --ai-cidr --ai-host --ai-vlan
            --wg-host --firewall --ntfy-url … ; --yes sin preguntas), sincroniza compose/.env y
            los define de nftables, exporta la CA de Caddy e imprime los siguientes pasos
  status    estado de los servicios (docker compose ps)
  doctor    comprobaciones de salud y seguridad (requiere root)
            doctor --json | --report (envía el resultado a Vector → panel "Guardian · Estado")
            doctor schedule apply|remove (timer cada 15 min con --report)
  version   versión
  key       llaves virtuales del gateway LiteLLM:
              key create --name <agente> --models m1,m2 [--budget USD] [--rpm N] [--duration 30d]
              key list | key delete <sk-...>
  policy    policy render egress    allowlists de Squid y Blocky desde agents/*.yaml
            policy render nftables  define de red desde guardian.yaml
            policy render fortios|opnsense|unifi [--out f]  política del firewall perimetral
  agent     agent run <nombre|manifiesto.yaml> [--dry-run]   lanza un agente en el sandbox
            agent schedule apply|list|show <n>|remove <n>    schedule.cron → timers de systemd
  secret    vault cifrado con age: secret init | set <ref> | get <ref> | list | rm <ref>
            (los manifiestos referencian secretos como vault:<ref>)
  backup    copias con restic (backup: en guardian.yaml): backup init | run | list |
            restore [snapshot] --to <dir> | schedule apply|remove`)
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
	// Archivos adicionales activados por guardian.yaml (p. ej. tls-acme-dns.yml).
	if raw, err := os.ReadFile(filepath.Join(root, "compose", ".extra-files")); err == nil {
		fields := strings.Fields(string(raw))
		for i := 0; i+1 < len(fields); i += 2 {
			if fields[i] == "-f" {
				args = append(args, "-f", filepath.Join(root, fields[i+1]))
			}
		}
	}
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

type checkResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail,omitempty"`
}

type doctorReport struct {
	Timestamp string        `json:"timestamp"`
	Version   string        `json:"version"`
	Host      string        `json:"host"`
	OK        int           `json:"ok"`
	Warn      int           `json:"warn"`
	Failed    int           `json:"failed"`
	Checks    []checkResult `json:"checks"`
}

func cmdDoctor(args []string) int {
	if len(args) > 0 && args[0] == "schedule" {
		return cmdDoctorSchedule(args[1:])
	}
	asJSON, report := false, false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "--report":
			report = true
		}
	}
	root := repoRoot()
	checks := []check{
		{"docker disponible", checkDocker},
		{"los 13 servicios en ejecución", checkServicesRunning},
		{"ningún socket del host escucha en 11434", checkNoHostOllama},
		{"contenedores gd-* solo publican 443/tcp y 51820/udp", checkPublishedPorts},
		{"existe compose/certs/root.crt", checkRootCert},
		{"Ollama responde desde gd_ai (open-webui → http://ollama:11434/api/tags)", checkOllamaFromWebUI},
		{"443 presenta un certificado emitido por la CA interna", checkTLSIssuedByInternalCA},
		{"gd_agents es una red interna (sin ruta a Internet)", checkAgentsNetworkInternal},
		{"un contenedor en gd_agents no alcanza Internet ni el host directamente", checkAgentsIsolation},
		{"ningún contenedor de agente tiene el socket de Docker", checkNoDockerSockInAgents},
		{"solo docker-socket-proxy monta el socket de Docker (y de solo lectura)", checkDockerSockOnlyProxy},
		{"Loki está listo e ingiere logs recientes", checkLokiIngesting},
		{"alertas: destino ntfy configurado", checkNtfyConfigured},
		{"copias de seguridad configuradas y recientes", checkBackupFresh},
		{"entorno de ejecución (LXC de Proxmox: nesting, tun, wireguard, AppArmor)", checkContainerHost},
	}
	host, _ := os.Hostname()
	rep := doctorReport{Timestamp: time.Now().UTC().Format(time.RFC3339), Version: version, Host: host}
	quiet := asJSON || report
	for _, c := range checks {
		err := c.fn(root)
		var w errWarn
		r := checkResult{Name: c.name, Status: "ok"}
		switch {
		case err == nil:
			rep.OK++
			if !quiet {
				fmt.Printf("[ OK ] %s\n", c.name)
			}
		case errors.As(err, &w):
			rep.Warn++
			r.Status, r.Detail = "warn", w.msg
			if !quiet {
				fmt.Printf("[WARN] %s\n       %s\n", c.name, w.msg)
			}
		default:
			rep.Failed++
			r.Status, r.Detail = "fail", err.Error()
			if !quiet {
				fmt.Printf("[FAIL] %s\n       %v\n", c.name, err)
			}
		}
		rep.Checks = append(rep.Checks, r)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rep)
	}
	if report {
		if err := postDoctorReport(rep); err != nil {
			fmt.Fprintln(os.Stderr, "doctor --report:", err)
		} else if !asJSON {
			fmt.Printf("doctor: %d ok, %d avisos, %d fallos → enviado a Vector\n", rep.OK, rep.Warn, rep.Failed)
		}
	}
	if rep.Failed > 0 {
		if !quiet {
			fmt.Printf("\ndoctor: %d comprobación(es) fallida(s)\n", rep.Failed)
		}
		return 1
	}
	if !quiet {
		fmt.Println("\ndoctor: todo correcto")
	}
	return 0
}

// postDoctorReport envía el informe al http_server de Vector (puerto 8688, ruta /doctor) usando
// la IP del contenedor en gd_audit: el host llega a las redes internas por su bridge.
func postDoctorReport(rep doctorReport) error {
	ip, err := run("docker", "inspect", "gd-vector", "--format", "{{(index .NetworkSettings.Networks \"gd_audit\").IPAddress}}")
	if err != nil {
		return fmt.Errorf("IP de gd-vector: %v", err)
	}
	ip = strings.TrimSpace(ip)
	body, _ := json.Marshal(rep)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("http://"+ip+":8688/doctor", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Vector respondió %d", resp.StatusCode)
	}
	return nil
}

func cmdDoctorSchedule(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "uso: guardianctl doctor schedule apply | remove")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "doctor schedule: requiere root")
		return 1
	}
	const sName, tName = "guardian-doctor.service", "guardian-doctor.timer"
	root := repoRoot()
	switch args[0] {
	case "apply":
		exe, _ := os.Executable()
		service := fmt.Sprintf("# Generado por guardianctl doctor schedule.\n[Unit]\nDescription=Guardian doctor (informe al panel de estado)\nAfter=docker.service\nRequires=docker.service\n\n[Service]\nType=oneshot\nWorkingDirectory=%s\nExecStart=%s doctor --report\nSuccessExitStatus=1\nTimeoutStartSec=10m\n", root, exe)
		timer := "# Generado por guardianctl doctor schedule.\n[Unit]\nDescription=Planificación de guardian doctor\n\n[Timer]\nOnBootSec=5m\nOnUnitActiveSec=15m\nRandomizedDelaySec=1m\nUnit=" + sName + "\n\n[Install]\nWantedBy=timers.target\n"
		if err := os.WriteFile(filepath.Join(unitDir, sName), []byte(service), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(filepath.Join(unitDir, tName), []byte(timer), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		exec.Command("systemctl", "daemon-reload").Run()
		if out, err := exec.Command("systemctl", "enable", "--now", tName).CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, string(out))
			return 1
		}
		fmt.Println("timer guardian-doctor habilitado (cada 15 min, informe a Vector → Grafana · Estado)")
		return 0
	case "remove":
		exec.Command("systemctl", "disable", "--now", tName).Run()
		os.Remove(filepath.Join(unitDir, tName))
		os.Remove(filepath.Join(unitDir, sName))
		exec.Command("systemctl", "daemon-reload").Run()
		fmt.Println("timer guardian-doctor eliminado")
		return 0
	default:
		return 2
	}
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

// cmdInit crea guardian.yaml (flags o preguntas), sincroniza compose/.env y los define de
// nftables, exporta la CA interna de Caddy e imprime los siguientes pasos. Idempotente.
func cmdInit(args []string) int {
	root := repoRoot()
	c := defaultConfig()
	if existing, err := loadConfig(root); err == nil {
		c = existing
	}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.StringVar(&c.Domain, "domain", c.Domain, "dominio base (ai.home)")
	fs.StringVar(&c.TZ, "tz", c.TZ, "zona horaria")
	fs.StringVar(&c.LANCIDR, "lan", c.LANCIDR, "red LAN de usuarios")
	fs.IntVar(&c.AIVLAN, "ai-vlan", c.AIVLAN, "VLAN de la zona de IA")
	fs.StringVar(&c.AICIDR, "ai-cidr", c.AICIDR, "red de la zona de IA")
	fs.StringVar(&c.AIHostIP, "ai-host", c.AIHostIP, "IP del host Guardian en la zona de IA")
	fs.StringVar(&c.AIGatewayIP, "ai-gateway", c.AIGatewayIP, "puerta de enlace de la zona de IA")
	fs.StringVar(&c.RemoteEndp, "wg-host", c.RemoteEndp, "nombre DNS público o IP para WireGuard")
	fs.StringVar(&c.RemoteCIDR, "wg-cidr", c.RemoteCIDR, "red de los clientes WireGuard")
	fs.StringVar(&c.FWVendor, "firewall", c.FWVendor, "fortios | opnsense | unifi")
	fs.StringVar(&c.FWWANIP, "wan-ip", c.FWWANIP, "IP WAN (para la VIP de FortiOS)")
	fs.IntVar(&c.RetentionDays, "retention-days", c.RetentionDays, "días de retención de logs")
	fs.StringVar(&c.NtfyURL, "ntfy-url", c.NtfyURL, "servidor ntfy (vacío = sin alertas)")
	fs.StringVar(&c.NtfyTopic, "ntfy-topic", c.NtfyTopic, "topic de ntfy")
	fs.StringVar(&c.BackupRepo, "backup-repo", c.BackupRepo, "repositorio restic (ruta, sftp:, s3:…); vacío = sin copias")
	fs.StringVar(&c.BackupCron, "backup-schedule", c.BackupCron, "cron de las copias (0 3 * * *)")
	fs.StringVar(&c.TLSMode, "tls-mode", c.TLSMode, "internal | acme-dns (dominio público con DNS-01)")
	fs.StringVar(&c.DNSProvider, "dns-provider", c.DNSProvider, "cloudflare | duckdns (con acme-dns)")
	fs.StringVar(&c.ACMEEmail, "acme-email", c.ACMEEmail, "correo para Let's Encrypt (con acme-dns)")
	noPrompt := fs.Bool("yes", false, "no preguntar; usar flags y valores por defecto")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*noPrompt && fs.NFlag() == 0 && isTerminal(os.Stdin) {
		r := bufio.NewReader(os.Stdin)
		fmt.Println("Configuración de Guardian (Enter acepta el valor entre corchetes):")
		c.Domain = prompt(r, "Dominio base", c.Domain)
		c.TZ = prompt(r, "Zona horaria", c.TZ)
		c.LANCIDR = prompt(r, "Red LAN de usuarios", c.LANCIDR)
		c.AICIDR = prompt(r, "Red de la zona de IA", c.AICIDR)
		c.AIHostIP = prompt(r, "IP de este host en la zona de IA", c.AIHostIP)
		c.AIGatewayIP = prompt(r, "Puerta de enlace de la zona de IA", firstHost(c.AICIDR))
		if v := prompt(r, "VLAN de la zona de IA", fmt.Sprint(c.AIVLAN)); v != "" {
			fmt.Sscanf(v, "%d", &c.AIVLAN)
		}
		c.RemoteEndp = prompt(r, "Nombre DNS público o IP para WireGuard", c.RemoteEndp)
		c.FWVendor = prompt(r, "Firewall perimetral (fortios/opnsense/unifi)", c.FWVendor)
		c.NtfyURL = prompt(r, "Servidor ntfy para alertas (vacío = ninguno)", c.NtfyURL)
		if c.NtfyURL != "" {
			c.NtfyTopic = prompt(r, "Topic de ntfy", c.NtfyTopic)
		}
	}
	// Si no se fijó la puerta de enlace y la heredada no cae en la red de IA, se recalcula.
	gwSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "ai-gateway" {
			gwSet = true
		}
	})
	if _, aiNet, err := net.ParseCIDR(c.AICIDR); err == nil && !gwSet {
		if gw := net.ParseIP(c.AIGatewayIP); gw == nil || !aiNet.Contains(gw) {
			c.AIGatewayIP = firstHost(c.AICIDR)
		}
	}
	if err := c.validate(); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		return 1
	}
	if err := saveConfig(root, c); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		return 1
	}
	fmt.Println("escrito", configPath(root))
	if err := syncEnv(root, c); err != nil {
		fmt.Fprintln(os.Stderr, "init: compose/.env:", err)
		return 1
	}
	fmt.Println("sincronizado compose/.env (DOMAIN, TZ, WG_HOST, LOKI_RETENTION_PERIOD, NTFY_*, ACME_*)")
	if c.TLSMode == "acme-dns" {
		fmt.Printf("modo acme-dns: pon ACME_DNS_TOKEN=<token de %s> en compose/.env; compose usará compose/tls-acme-dns.yml (imagen de Caddy construida en local)\n", c.DNSProvider)
	}
	changed, err := renderNftables(root, c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "init: nftables:", err)
		return 1
	}
	if len(changed) > 0 {
		fmt.Println("actualizados define de", strings.Join(changed, ", "))
	}
	if err := exportCA(root); err != nil {
		fmt.Fprintln(os.Stderr, "aviso: CA no exportada todavía:", err, "(install.sh lo hará al levantar caddy)")
	}
	printNextSteps(root)
	return 0
}

// exportCA levanta solo caddy y copia su CA raíz a compose/certs/root.crt.
func exportCA(root string) error {
	if err := checkDocker(root); err != nil {
		return err
	}
	certDir := filepath.Join(root, "compose", "certs")
	dst := filepath.Join(certDir, "root.crt")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return err
	}
	if _, err := run("docker", composeArgs(root, "up", "-d", "caddy")...); err != nil {
		return fmt.Errorf("no se pudo levantar caddy: %v", err)
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
		return fmt.Errorf("caddy no generó la CA: %v", lastErr)
	}
	_ = os.Chmod(dst, 0o644)
	fmt.Println("CA interna exportada a", dst)
	return nil
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
	fw := "fortios"
	if c, err := loadConfig(root); err == nil {
		fw = c.FWVendor
	}
	fmt.Printf(`
Siguientes pasos:
  1. DNS local: %s, id.%s y vpn.%s → IP de este host.
  2. Instala la CA compose/certs/root.crt en tus dispositivos (docs/instalacion.md).
  3. Pocket ID: https://id.%s/setup → admin con passkey y cliente OIDC "open-webui"
     con callback https://%s/oauth/oidc/callback
  4. Copia OAUTH_CLIENT_ID / OAUTH_CLIENT_SECRET a compose/.env y ejecuta: make restart
  5. WireGuard: https://vpn.%s → asistente (host %s, puerto 51820) → QR para el móvil.
  6. Firewall perimetral: guardianctl policy render %s   (y en el host: sudo ./install.sh --with-nftables)
  7. Comprueba: make doctor
`, domain, domain, domain, domain, domain, domain, wgHost, fw)
}

// ---------------------------------------------------------------- key (LiteLLM)

// apiClient devuelve un cliente HTTPS que habla con api.<DOMAIN> a través de Caddy en
// 127.0.0.1:443, verificando con la CA interna. Es el mismo camino que usan las apps.
func apiClient(root string) (*http.Client, string, error) {
	domain := envValue(root, "DOMAIN")
	if domain == "" {
		return nil, "", errors.New("DOMAIN no definido en compose/.env")
	}
	pem, err := os.ReadFile(filepath.Join(root, "compose", "certs", "root.crt"))
	if err != nil {
		return nil, "", err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, "", errors.New("compose/certs/root.crt no es un PEM válido")
	}
	host := "api." + domain
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: host, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, "127.0.0.1:443")
		},
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}, "https://" + host, nil
}

func litellmCall(root, method, path string, body any) (int, map[string]any, error) {
	client, base, err := apiClient(root)
	if err != nil {
		return 0, nil, err
	}
	master := envValue(root, "LITELLM_MASTER_KEY")
	if master == "" {
		return 0, nil, errors.New("LITELLM_MASTER_KEY no definido en compose/.env")
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+master)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			out["raw"] = string(raw)
		}
	}
	return resp.StatusCode, out, nil
}

func cmdKey(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "uso: guardianctl key create|list|delete …")
		return 2
	}
	root := repoRoot()
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("key create", flag.ContinueOnError)
		name := fs.String("name", "", "nombre del agente o app (alias de la llave)")
		models := fs.String("models", "", "modelos permitidos, separados por comas")
		budget := fs.Float64("budget", 0, "presupuesto máximo en USD (0 = sin límite)")
		rpm := fs.Int("rpm", 0, "peticiones por minuto (0 = sin límite)")
		duration := fs.String("duration", "", "caducidad, p. ej. 30d (vacío = no caduca)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *name == "" || *models == "" {
			fmt.Fprintln(os.Stderr, "key create: --name y --models son obligatorios")
			return 2
		}
		body := map[string]any{
			"key_alias": *name,
			"models":    strings.Split(*models, ","),
			"metadata":  map[string]any{"guardian_agent": *name},
		}
		if *budget > 0 {
			body["max_budget"] = *budget
		}
		if *rpm > 0 {
			body["rpm_limit"] = *rpm
		}
		if *duration != "" {
			body["duration"] = *duration
		}
		code, out, err := litellmCall(root, http.MethodPost, "/key/generate", body)
		if err != nil {
			fmt.Fprintln(os.Stderr, "key create:", err)
			return 1
		}
		if code != 200 {
			fmt.Fprintf(os.Stderr, "key create: HTTP %d: %v\n", code, out)
			return 1
		}
		fmt.Printf("%s\n", out["key"])
		fmt.Fprintf(os.Stderr, "llave creada para %s (modelos: %s). Guárdala: no se vuelve a mostrar.\n", *name, *models)
		return 0
	case "list":
		code, out, err := litellmCall(root, http.MethodGet, "/key/list?return_full_object=true&size=100", nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "key list:", err)
			return 1
		}
		if code != 200 {
			fmt.Fprintf(os.Stderr, "key list: HTTP %d: %v\n", code, out)
			return 1
		}
		keys, _ := out["keys"].([]any)
		fmt.Printf("%-20s %-14s %-10s %s\n", "ALIAS", "TOKEN", "GASTO", "MODELOS")
		for _, k := range keys {
			m, _ := k.(map[string]any)
			alias, _ := m["key_alias"].(string)
			token, _ := m["token"].(string)
			if len(token) > 12 {
				token = token[:12] + "…"
			}
			spend, _ := m["spend"].(float64)
			var models []string
			if ms, ok := m["models"].([]any); ok {
				for _, x := range ms {
					models = append(models, fmt.Sprint(x))
				}
			}
			fmt.Printf("%-20s %-14s %-10.4f %s\n", alias, token, spend, strings.Join(models, ","))
		}
		return 0
	case "delete":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "uso: guardianctl key delete <sk-...|alias>")
			return 2
		}
		body := map[string]any{"keys": []string{args[1]}}
		if !strings.HasPrefix(args[1], "sk-") {
			body = map[string]any{"key_aliases": []string{args[1]}}
		}
		code, out, err := litellmCall(root, http.MethodPost, "/key/delete", body)
		if err != nil {
			fmt.Fprintln(os.Stderr, "key delete:", err)
			return 1
		}
		if code != 200 {
			fmt.Fprintf(os.Stderr, "key delete: HTTP %d: %v\n", code, out)
			return 1
		}
		fmt.Println("llave eliminada")
		return 0
	default:
		fmt.Fprintln(os.Stderr, "key: subcomando desconocido:", args[0])
		return 2
	}
}

// ---------------------------------------------------------------- doctor (Fase 2)

const probeImage = "busybox:1.37"

func checkAgentsNetworkInternal(string) error {
	out, err := run("docker", "network", "inspect", "gd_agents", "--format", "{{.Internal}}")
	if err != nil {
		return fmt.Errorf("la red gd_agents no existe: %v", err)
	}
	if strings.TrimSpace(out) != "true" {
		return errors.New("gd_agents no es internal: los agentes tendrían ruta por defecto")
	}
	return nil
}

// checkAgentsIsolation lanza una sonda efímera en gd_agents (sin política de egreso) y
// comprueba que no conecta a Internet. Que tampoco llegue al gateway del host depende del
// firewall (--with-nftables), así que eso es un aviso, no un fallo.
func checkAgentsIsolation(string) error {
	probe := func(target string) bool {
		_, err := run("docker", "run", "--rm", "--network", "gd_agents", "--dns", agentsDNS,
			"--cap-drop", "ALL", "--user", agentUID, "--read-only", probeImage,
			"nc", "-z", "-w", "3", target, "443")
		return err == nil // conectó
	}
	if _, err := run("docker", "image", "inspect", probeImage); err != nil {
		if _, err := run("docker", "pull", "-q", probeImage); err != nil {
			return fmt.Errorf("no se pudo obtener la imagen de sonda %s: %v", probeImage, err)
		}
	}
	if probe("1.1.1.1") {
		return errors.New("una sonda en gd_agents conectó a 1.1.1.1:443 sin pasar por el proxy")
	}
	if probe("172.28.30.1") {
		return errWarn{"la sonda alcanzó 172.28.30.1:443 (docker-proxy del host). Aplica el firewall: sudo ./install.sh --with-nftables"}
	}
	return nil
}

func checkNoDockerSockInAgents(string) error {
	out, err := run("docker", "ps", "-q", "--filter", "label=guardian.agent")
	if err != nil {
		return err
	}
	ids := strings.Fields(out)
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"inspect", "--format", "{{.Name}} {{range .Mounts}}{{.Source}} {{end}}"}, ids...)
	out, err = run("docker", args...)
	if err != nil {
		return err
	}
	var bad []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(line, "docker.sock") {
			bad = append(bad, strings.Fields(line)[0])
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("agentes con docker.sock montado: %s", strings.Join(bad, ", "))
	}
	return nil
}

// ---------------------------------------------------------------- doctor (Fase 3)

func checkDockerSockOnlyProxy(string) error {
	out, err := run("docker", "ps", "-q")
	if err != nil {
		return err
	}
	ids := strings.Fields(out)
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"inspect", "--format", "{{.Name}}|{{range .Mounts}}{{.Source}}:{{.RW}} {{end}}"}, ids...)
	out, err = run("docker", args...)
	if err != nil {
		return err
	}
	var bad []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, mounts, _ := strings.Cut(line, "|")
		name = strings.TrimPrefix(name, "/")
		for _, m := range strings.Fields(mounts) {
			if !strings.Contains(m, "docker.sock") {
				continue
			}
			if name != "gd-docker-socket-proxy" || strings.HasSuffix(m, ":true") {
				bad = append(bad, name+" ("+m+")")
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("socket de Docker montado donde no debe: %s", strings.Join(bad, ", "))
	}
	return nil
}

func checkLokiIngesting(string) error {
	if _, err := run("docker", "image", "inspect", probeImage); err != nil {
		if _, err := run("docker", "pull", "-q", probeImage); err != nil {
			return fmt.Errorf("no se pudo obtener la imagen de sonda %s: %v", probeImage, err)
		}
	}
	// Loki tarda 1–2 minutos en declararse listo tras arrancar (anillo del ingester): se reintenta.
	var out string
	var err error
	for attempt := 0; attempt < 9; attempt++ {
		out, err = run("docker", "run", "--rm", "--network", "gd_audit", "--cap-drop", "ALL", "--user", agentUID, probeImage,
			"wget", "-q", "-O", "-", "http://loki:3100/ready")
		if err == nil && strings.Contains(out, "ready") {
			break
		}
		time.Sleep(10 * time.Second)
	}
	if err != nil || !strings.Contains(out, "ready") {
		return fmt.Errorf("Loki no responde ready en gd_audit tras 90 s: %v %s", err, strings.TrimSpace(out))
	}
	// Alguna línea de cualquier servicio en los últimos 15 minutos.
	start := time.Now().Add(-15 * time.Minute).UnixNano()
	q := fmt.Sprintf("http://loki:3100/loki/api/v1/query_range?query=%s&limit=1&start=%d",
		"%7Bservice%3D~%22.%2B%22%7D", start)
	out, err = run("docker", "run", "--rm", "--network", "gd_audit", "--cap-drop", "ALL", "--user", agentUID, probeImage,
		"wget", "-q", "-O", "-", q)
	if err != nil {
		return fmt.Errorf("consulta a Loki falló: %v", err)
	}
	if !strings.Contains(out, `"values"`) {
		return errWarn{"Loki responde pero no tiene logs de los últimos 15 minutos; revisa gd-vector"}
	}
	return nil
}

func checkNtfyConfigured(root string) error {
	if envValue(root, "NTFY_URL") == "" {
		return errWarn{"NTFY_URL vacío en compose/.env: las alertas de Grafana no llegan a ningún sitio"}
	}
	return nil
}

// checkContainerHost: si Guardian corre dentro de un contenedor LXC (Proxmox), comprueba lo que
// el sandbox y WireGuard necesitan del host. En metal o VM no aplica.
func checkContainerHost(string) error {
	env, _ := os.ReadFile("/proc/1/environ")
	inLXC := strings.Contains(string(env), "container=lxc")
	if !inLXC {
		if raw, err := os.ReadFile("/proc/1/cgroup"); err == nil && strings.Contains(string(raw), "/lxc/") {
			inLXC = true
		}
	}
	if !inLXC {
		return nil
	}
	var problems []string
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		problems = append(problems, "falta /dev/net/tun (wg-easy): añade lxc.cgroup2.devices.allow y lxc.mount.entry para tun")
	}
	if out, err := run("sh", "-c", "modprobe wireguard 2>/dev/null; grep -qw wireguard /proc/modules && echo ok"); err != nil || !strings.Contains(out, "ok") {
		problems = append(problems, "módulo wireguard no cargado: cárgalo en el HOST Proxmox (modprobe wireguard) y persístelo en /etc/modules")
	}
	if _, err := os.Stat("/sys/kernel/security/apparmor"); err != nil {
		problems = append(problems, "AppArmor no visible dentro del contenedor: el sandbox de agentes lo exige (lxc.apparmor.profile / nesting)")
	}
	if len(problems) > 0 {
		return fmt.Errorf("LXC detectado; %s (ver docs/proxmox-lxc.md)", strings.Join(problems, "; "))
	}
	return errWarn{"LXC detectado: nesting, tun, wireguard y AppArmor presentes. Recuerda que el contenedor debe ser privilegiado o tener keyctl/nesting activados (docs/proxmox-lxc.md)"}
}
