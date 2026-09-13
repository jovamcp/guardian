// guardian.yaml: fuente de verdad de la red (DESIGN.md §6). De aquí derivan compose/.env
// (valores no secretos) y los `define` de nftables.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Config struct {
	Domain        string
	TZ            string
	LANCIDR       string
	AIVLAN        int
	AICIDR        string
	AIHostIP      string
	AIGatewayIP   string
	RemoteProv    string
	RemoteCIDR    string
	RemoteEndp    string
	RuntimeKind   string
	RuntimeGPU    string
	FWVendor      string
	FWParent      string
	FWLAN         string
	FWWAN         string
	FWWANIP       string
	RetentionDays int
	NtfyURL       string
	NtfyTopic     string
	// tls (v0.2)
	TLSMode     string // internal | acme-dns
	DNSProvider string // cloudflare | duckdns
	ACMEEmail   string
	// backup (v0.2)
	BackupRepo     string // ruta local o URL de restic (sftp:, s3:, rest:…); vacío = sin copias
	BackupPassword string // vault:<ref>
	BackupCron     string
	BackupModels   bool // incluir ollama_data (modelos, grandes y redescargables)
	KeepDaily      int
	KeepWeekly     int
	KeepMonthly    int
}

func defaultConfig() Config {
	return Config{
		Domain: "ai.home", TZ: "America/Puerto_Rico",
		LANCIDR: "10.10.10.0/24",
		AIVLAN:  20, AICIDR: "10.20.0.0/24", AIHostIP: "10.20.0.10", AIGatewayIP: "10.20.0.1",
		RemoteProv: "wireguard", RemoteCIDR: "10.8.0.0/24",
		RuntimeKind: "ollama", RuntimeGPU: "none",
		FWVendor: "fortios", FWParent: "internal", FWLAN: "internal", FWWAN: "wan1", FWWANIP: "0.0.0.0",
		RetentionDays: 30, NtfyTopic: "guardian",
		TLSMode: "internal", DNSProvider: "cloudflare",
		BackupPassword: "vault:backup/restic", BackupCron: "0 3 * * *",
		KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6,
	}
}

func configPath(root string) string { return filepath.Join(root, "guardian.yaml") }

func loadConfig(root string) (Config, error) {
	c := defaultConfig()
	raw, err := os.ReadFile(configPath(root))
	if err != nil {
		return c, err
	}
	doc, err := parseYAML(string(raw))
	if err != nil {
		return c, fmt.Errorf("guardian.yaml: %v", err)
	}
	str := func(v any, def string) string {
		if s := ystr(v); s != "" {
			return s
		}
		return def
	}
	num := func(v any, def int) int {
		if n, ok := v.(int64); ok {
			return int(n)
		}
		if s := ystr(v); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				return n
			}
		}
		return def
	}
	c.Domain = str(doc["domain"], c.Domain)
	c.TZ = str(doc["tz"], c.TZ)
	c.LANCIDR = str(doc["lan_cidr"], c.LANCIDR)
	ai := ymap(doc["ai_zone"])
	c.AIVLAN = num(ai["vlan"], c.AIVLAN)
	c.AICIDR = str(ai["cidr"], c.AICIDR)
	c.AIHostIP = str(ai["host_ip"], c.AIHostIP)
	c.AIGatewayIP = str(ai["gateway_ip"], "")
	rm := ymap(doc["remote"])
	c.RemoteProv = str(rm["provider"], c.RemoteProv)
	c.RemoteCIDR = str(rm["cidr"], c.RemoteCIDR)
	c.RemoteEndp = str(rm["endpoint"], "")
	rt := ymap(doc["runtime"])
	c.RuntimeKind = str(rt["kind"], c.RuntimeKind)
	c.RuntimeGPU = str(rt["gpu"], c.RuntimeGPU)
	fw := ymap(doc["firewall"])
	c.FWVendor = str(fw["vendor"], c.FWVendor)
	c.FWParent = str(fw["parent_interface"], c.FWParent)
	c.FWLAN = str(fw["lan_interface"], c.FWLAN)
	c.FWWAN = str(fw["wan_interface"], c.FWWAN)
	c.FWWANIP = str(fw["wan_ip"], c.FWWANIP)
	c.RetentionDays = num(ymap(doc["audit"])["retention_days"], c.RetentionDays)
	nt := ymap(ymap(doc["alerts"])["ntfy"])
	c.NtfyURL = str(nt["url"], "")
	c.NtfyTopic = str(nt["topic"], c.NtfyTopic)
	tl := ymap(doc["tls"])
	c.TLSMode = str(tl["mode"], c.TLSMode)
	c.DNSProvider = str(tl["dns_provider"], c.DNSProvider)
	c.ACMEEmail = str(tl["email"], "")
	bk := ymap(doc["backup"])
	c.BackupRepo = str(bk["repository"], "")
	c.BackupPassword = str(bk["password"], c.BackupPassword)
	c.BackupCron = str(bk["schedule"], c.BackupCron)
	if v, ok := bk["include_models"].(bool); ok {
		c.BackupModels = v
	}
	keep := ymap(bk["keep"])
	c.KeepDaily = num(keep["daily"], c.KeepDaily)
	c.KeepWeekly = num(keep["weekly"], c.KeepWeekly)
	c.KeepMonthly = num(keep["monthly"], c.KeepMonthly)
	if c.AIGatewayIP == "" {
		c.AIGatewayIP = firstHost(c.AICIDR)
	}
	return c, c.validate()
}

func (c Config) validate() error {
	if !regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`).MatchString(c.Domain) {
		return fmt.Errorf("domain %q inválido (p. ej. ai.home)", c.Domain)
	}
	for name, cidr := range map[string]string{"lan_cidr": c.LANCIDR, "ai_zone.cidr": c.AICIDR, "remote.cidr": c.RemoteCIDR} {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("%s %q inválido", name, cidr)
		}
	}
	_, aiNet, _ := net.ParseCIDR(c.AICIDR)
	for name, ip := range map[string]string{"ai_zone.host_ip": c.AIHostIP, "ai_zone.gateway_ip": c.AIGatewayIP} {
		p := net.ParseIP(ip)
		if p == nil || !aiNet.Contains(p) {
			return fmt.Errorf("%s %q debe estar dentro de %s", name, ip, c.AICIDR)
		}
	}
	if c.AIVLAN < 1 || c.AIVLAN > 4094 {
		return fmt.Errorf("ai_zone.vlan %d fuera de 1-4094", c.AIVLAN)
	}
	if c.RemoteProv != "wireguard" && c.RemoteProv != "tailscale" {
		return errors.New("remote.provider debe ser wireguard o tailscale")
	}
	if c.RetentionDays < 1 || c.RetentionDays > 3650 {
		return errors.New("audit.retention_days debe estar entre 1 y 3650")
	}
	if c.FWWANIP != "" && net.ParseIP(c.FWWANIP) == nil {
		return fmt.Errorf("firewall.wan_ip %q inválida", c.FWWANIP)
	}
	if c.TLSMode != "internal" && c.TLSMode != "acme-dns" {
		return errors.New("tls.mode debe ser internal o acme-dns")
	}
	if c.TLSMode == "acme-dns" {
		if c.DNSProvider != "cloudflare" && c.DNSProvider != "duckdns" {
			return errors.New("tls.dns_provider debe ser cloudflare o duckdns")
		}
		if !strings.Contains(c.ACMEEmail, "@") {
			return errors.New("tls.email es obligatorio con acme-dns (avisos de Let's Encrypt)")
		}
		if strings.HasSuffix(c.Domain, ".home") || strings.HasSuffix(c.Domain, ".lan") || strings.HasSuffix(c.Domain, ".local") {
			return fmt.Errorf("tls.mode acme-dns necesita un dominio público que controles; %q no lo es", c.Domain)
		}
	}
	if c.BackupRepo != "" && !strings.HasPrefix(c.BackupPassword, "vault:") {
		return errors.New("backup.password debe ser una referencia vault:<ref> (regla dura 6)")
	}
	return nil
}

func firstHost(cidr string) string {
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	ip4 := n.IP.To4()
	if ip4 == nil {
		return ip.String()
	}
	ip4[3]++
	return ip4.String()
}

func (c Config) yaml() string {
	return fmt.Sprintf(`# guardian.yaml — fuente de verdad de la red de Guardian (generado por guardianctl init).
# Edita y ejecuta: guardianctl policy render nftables  (y make restart si cambió domain/tz/wg_host)
domain: %s
tz: %s

# Red de usuarios (LAN de casa).
lan_cidr: %s

# Zona de IA: VLAN dedicada donde vive el host Guardian.
ai_zone:
  vlan: %d
  cidr: %s
  host_ip: %s
  gateway_ip: %s

# Acceso remoto.
remote:
  provider: %s
  cidr: %s
  endpoint: %q

# Runtime de IA que Guardian protege.
runtime:
  kind: %s
  gpu: %s

# Firewall perimetral (policy render fortios|opnsense).
firewall:
  vendor: %s
  parent_interface: %s
  lan_interface: %s
  wan_interface: %s
  wan_ip: %s

audit:
  retention_days: %d

alerts:
  ntfy:
    url: %q
    topic: %s

# TLS: internal (CA propia de Caddy, por defecto) o acme-dns (dominio público, Let's Encrypt
# por DNS-01; el token del proveedor va en compose/.env como ACME_DNS_TOKEN).
tls:
  mode: %s
  dns_provider: %s
  email: %q

# Copias de seguridad con restic (guardianctl backup). repository vacío = desactivadas.
# Ejemplos: /var/backups/guardian  |  sftp:user@nas:/guardian  |  s3:s3.amazonaws.com/bucket
backup:
  repository: %q
  password: %s
  schedule: %q
  include_models: %t
  keep:
    daily: %d
    weekly: %d
    monthly: %d
`, c.Domain, c.TZ, c.LANCIDR, c.AIVLAN, c.AICIDR, c.AIHostIP, c.AIGatewayIP,
		c.RemoteProv, c.RemoteCIDR, c.RemoteEndp, c.RuntimeKind, c.RuntimeGPU,
		c.FWVendor, c.FWParent, c.FWLAN, c.FWWAN, c.FWWANIP, c.RetentionDays, c.NtfyURL, c.NtfyTopic,
		c.TLSMode, c.DNSProvider, c.ACMEEmail,
		c.BackupRepo, c.BackupPassword, c.BackupCron, c.BackupModels, c.KeepDaily, c.KeepWeekly, c.KeepMonthly)
}

func saveConfig(root string, c Config) error {
	return os.WriteFile(configPath(root), []byte(c.yaml()), 0o644)
}

// syncEnv escribe en compose/.env SOLO los valores no secretos derivados de guardian.yaml.
func syncEnv(root string, c Config) error {
	envPath := filepath.Join(root, "compose", ".env")
	if _, err := os.Stat(envPath); err != nil {
		src, err := os.ReadFile(filepath.Join(root, ".env.example"))
		if err != nil {
			return err
		}
		if err := os.WriteFile(envPath, src, 0o600); err != nil {
			return err
		}
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	values := map[string]string{
		"DOMAIN": c.Domain, "TZ": c.TZ, "WG_HOST": c.RemoteEndp,
		"LOKI_RETENTION_PERIOD": fmt.Sprintf("%dh", c.RetentionDays*24),
		"NTFY_URL":              c.NtfyURL, "NTFY_TOPIC": c.NtfyTopic,
		"ACME_EMAIL": c.ACMEEmail, "ACME_DNS_PROVIDER": c.DNSProvider,
	}
	// Archivos compose adicionales según el modo (los leen Makefile e install.sh).
	extra := filepath.Join(root, "compose", ".extra-files")
	if c.TLSMode == "acme-dns" {
		if err := os.WriteFile(extra, []byte("-f compose/tls-acme-dns.yml\n"), 0o644); err != nil {
			return err
		}
	} else {
		os.Remove(extra)
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		k, _, ok := strings.Cut(line, "=")
		if ok && !strings.HasPrefix(line, "#") {
			if v, found := values[strings.TrimSpace(k)]; found {
				out = append(out, k+"="+v)
				seen[k] = true
				continue
			}
		}
		out = append(out, line)
	}
	for k, v := range values {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}
	return os.WriteFile(envPath, []byte(strings.Join(out, "\n")+"\n"), 0o600)
}

// renderNftables reescribe los define de red en los dos rulesets a partir de guardian.yaml.
func renderNftables(root string, c Config) ([]string, error) {
	defs := map[string]string{"LAN_NET": c.LANCIDR, "AI_NET": c.AICIDR, "WG_NET": c.RemoteCIDR}
	re := regexp.MustCompile(`^(\s*define\s+)(LAN_NET|AI_NET|WG_NET)(\s*=\s*)(\S+)(.*)$`)
	var changed []string
	for _, f := range []string{"nftables/guardian.nft", "nftables/docker-user.nft"} {
		p := filepath.Join(root, f)
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(raw), "\n")
		dirty := false
		for i, l := range lines {
			m := re.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			if want := defs[m[2]]; m[4] != want {
				lines[i] = m[1] + m[2] + m[3] + want + m[5]
				dirty = true
			}
		}
		if dirty {
			if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
				return nil, err
			}
			changed = append(changed, f)
		}
	}
	return changed, nil
}

// prompt lee un valor por terminal con valor por defecto.
func prompt(r *bufio.Reader, label, def string) string {
	fmt.Printf("%s [%s]: ", label, def)
	s, _ := r.ReadString('\n')
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}
