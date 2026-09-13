// Manifiestos de agentes y renderizado de la política de egreso (Squid + Blocky).
package main

import (
	"errors"
	"fmt"
	"github.com/jovamcp/guardian/internal/yamlmini"
	"hash/fnv"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Constantes de red de gd_agents (deben coincidir con compose/docker-compose.yml).
const (
	agentsSubnet = "172.28.30.0/24"
	agentsDNS    = "172.28.30.53" // blocky
	agentsProxy  = "172.28.30.3"  // squid
	agentsLLM    = "172.28.30.10" // litellm
	agentIPFirst = 100            // primer host asignable a agentes
	agentIPLast  = 250
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
var digestRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
var domainRe = regexp.MustCompile(`^(\*\.)?([a-z0-9-]+\.)+[a-z]{2,}$`)

// Manifest es la parte del YAML que guardianctl entiende (DESIGN.md §5).
type Manifest struct {
	Name     string
	Image    string
	Models   []string
	KeyMode  string // auto | vault:<ruta> | ""
	Allow    []string
	Services map[string]Service
	CPUs     string
	Memory   string
	PIDs     string
	Cron     string
	Command  []string
	Mounts   []string // host:contenedor (siempre ro)
	Env      map[string]string
	IP       string
	Path     string
	Runtime  string // runc (por defecto) | gvisor
}

type Service struct {
	URL   string
	Token string
}

func loadManifest(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := yamlmini.Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	m := &Manifest{Path: path, Services: map[string]Service{}, Env: map[string]string{}}
	m.Name = yamlmini.Str(doc["name"])
	m.Image = yamlmini.Str(doc["image"])
	llm := yamlmini.Map(doc["llm"])
	m.Models = yamlmini.Strs(llm["models"])
	m.KeyMode = yamlmini.Str(llm["key"])
	m.Allow = yamlmini.Strs(yamlmini.Map(doc["egress"])["allow"])
	for k, v := range yamlmini.Map(doc["services"]) {
		sv := yamlmini.Map(v)
		m.Services[k] = Service{URL: yamlmini.Str(sv["url"]), Token: yamlmini.Str(sv["token"])}
	}
	res := yamlmini.Map(doc["resources"])
	m.CPUs, m.Memory, m.PIDs = yamlmini.Str(res["cpus"]), yamlmini.Str(res["memory"]), yamlmini.Str(res["pids"])
	m.Cron = yamlmini.Str(yamlmini.Map(doc["schedule"])["cron"])
	m.Command = yamlmini.Strs(doc["command"])
	m.Mounts = yamlmini.Strs(doc["mounts"])
	for k, v := range yamlmini.Map(doc["env"]) {
		m.Env[k] = yamlmini.Str(v)
	}
	m.IP = yamlmini.Str(yamlmini.Map(doc["network"])["ip"])
	m.Runtime = yamlmini.Str(yamlmini.Map(doc["sandbox"])["runtime"])
	if m.Runtime == "" {
		m.Runtime = "runc"
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if m.IP == "" {
		m.IP = assignIP(m.Name)
	}
	return m, nil
}

func (m *Manifest) validate() error {
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("name %q inválido (minúsculas, dígitos y guiones, máx. 32)", m.Name)
	}
	if m.Image == "" {
		return errors.New("image es obligatorio")
	}
	if !digestRe.MatchString(m.Image) {
		return errors.New("image debe ir fijada por digest completo (…@sha256:<64 hex>), regla dura 4")
	}
	for _, d := range m.Allow {
		if !domainRe.MatchString(d) {
			return fmt.Errorf("egress.allow: dominio inválido %q (solo nombres DNS, opcionalmente *.dominio)", d)
		}
	}
	for name, sv := range m.Services {
		if sv.Token != "" && !strings.HasPrefix(sv.Token, "vault:") {
			return fmt.Errorf("services.%s.token debe ser una referencia vault:<ruta>, nunca el secreto en claro (regla dura 6)", name)
		}
	}
	if m.KeyMode != "" && m.KeyMode != "auto" && !strings.HasPrefix(m.KeyMode, "vault:") {
		return errors.New("llm.key debe ser auto o vault:<ruta>")
	}
	if m.IP != "" {
		ip := net.ParseIP(m.IP)
		_, subnet, _ := net.ParseCIDR(agentsSubnet)
		if ip == nil || !subnet.Contains(ip) {
			return fmt.Errorf("network.ip %q fuera de %s", m.IP, agentsSubnet)
		}
	}
	if m.Runtime != "runc" && m.Runtime != "gvisor" {
		return fmt.Errorf("sandbox.runtime debe ser runc o gvisor (tienes %q)", m.Runtime)
	}
	for _, mnt := range m.Mounts {
		if strings.Count(mnt, ":") != 1 {
			return fmt.Errorf("mounts: formato origen:destino, sin opciones (%q); siempre se monta solo lectura", mnt)
		}
		src, dst, _ := strings.Cut(mnt, ":")
		if !filepath.IsAbs(dst) || dst == "/" {
			return fmt.Errorf("mounts: el destino debe ser una ruta absoluta dentro del contenedor (%q)", mnt)
		}
		for _, forbidden := range []string{"/run/guardian", "/proc", "/sys", "/dev"} {
			if dst == forbidden || strings.HasPrefix(dst, forbidden+"/") {
				return fmt.Errorf("mounts: destino %q no permitido", dst)
			}
		}
		if filepath.IsAbs(src) {
			return fmt.Errorf("mounts: el origen debe ser relativo al manifiesto y quedar dentro de agents/ (%q)", src)
		}
	}
	return nil
}

// resolveMount devuelve el origen absoluto de un mount comprobando que queda dentro de agents/
// del repo (sin enlaces simbólicos hacia fuera) y que no es un socket ni un secreto.
func resolveMount(root, manifestPath, src string) (string, error) {
	base := filepath.Dir(manifestPath)
	abs, err := filepath.EvalSymlinks(filepath.Join(base, src))
	if err != nil {
		return "", fmt.Errorf("mounts: %s: %v", src, err)
	}
	agentsDir, err := filepath.EvalSymlinks(filepath.Join(root, "agents"))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(agentsDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("mounts: %s resuelve fuera de %s (%s)", src, agentsDir, abs)
	}
	if strings.Contains(abs, "docker.sock") {
		return "", errors.New("mounts: el socket de Docker nunca se monta en un agente")
	}
	return abs, nil
}

// assignIP deriva una IP estable del nombre (172.28.30.100–250). Si dos agentes chocan,
// `policy render` lo detecta y pide fijar network.ip en uno de ellos.
func assignIP(name string) string {
	h := fnv.New32a()
	h.Write([]byte(name))
	host := agentIPFirst + int(h.Sum32()%uint32(agentIPLast-agentIPFirst+1))
	return fmt.Sprintf("172.28.30.%d", host)
}

// loadAllManifests lee agents/*.yaml y agents/examples/*.yaml.
func loadAllManifests(root string) ([]*Manifest, error) {
	var paths []string
	for _, pat := range []string{"agents/*.yaml", "agents/*.yml", "agents/examples/*.yaml", "agents/examples/*.yml"} {
		got, _ := filepath.Glob(filepath.Join(root, pat))
		paths = append(paths, got...)
	}
	sort.Strings(paths)
	var out []*Manifest
	seenName := map[string]string{}
	seenIP := map[string]string{}
	for _, p := range paths {
		m, err := loadManifest(p)
		if err != nil {
			return nil, err
		}
		if prev, ok := seenName[m.Name]; ok {
			return nil, fmt.Errorf("agente %q definido en %s y %s", m.Name, prev, p)
		}
		if prev, ok := seenIP[m.IP]; ok {
			return nil, fmt.Errorf("colisión de IP %s entre %s y %s: fija network.ip en uno de ellos", m.IP, prev, m.Name)
		}
		seenName[m.Name], seenIP[m.IP] = p, m.Name
		out = append(out, m)
	}
	return out, nil
}

// findManifest localiza un agente por nombre o por ruta.
func findManifest(root, nameOrPath string) (*Manifest, error) {
	if strings.HasSuffix(nameOrPath, ".yaml") || strings.HasSuffix(nameOrPath, ".yml") {
		return loadManifest(nameOrPath)
	}
	all, err := loadAllManifests(root)
	if err != nil {
		return nil, err
	}
	for _, m := range all {
		if m.Name == nameOrPath {
			return m, nil
		}
	}
	return nil, fmt.Errorf("no existe el agente %q en agents/ ni agents/examples/", nameOrPath)
}

// ---------------------------------------------------------------- policy render egress

func renderSquid(ms []*Manifest) string {
	var b strings.Builder
	b.WriteString("# Generado por `guardianctl policy render egress`. No editar a mano.\n")
	if len(ms) == 0 {
		b.WriteString("# (vacío: ningún agente registrado todavía)\n")
		return b.String()
	}
	for _, m := range ms {
		id := "agent_" + strings.ReplaceAll(m.Name, "-", "_")
		fmt.Fprintf(&b, "\n# %s (%s)\n", m.Name, filepath.Base(m.Path))
		fmt.Fprintf(&b, "acl %s src %s/32\n", id, m.IP)
		if len(m.Allow) == 0 {
			fmt.Fprintf(&b, "# sin egress.allow: este agente no sale a Internet\n")
			continue
		}
		var doms []string
		for _, d := range m.Allow {
			// En Squid, ".dominio" cubre el dominio y sus subdominios; "*.dominio" del manifiesto → ".dominio".
			if strings.HasPrefix(d, "*.") {
				doms = append(doms, "."+strings.TrimPrefix(d, "*."))
			} else {
				doms = append(doms, d)
			}
		}
		fmt.Fprintf(&b, "acl %s_dst dstdomain %s\n", id, strings.Join(doms, " "))
		fmt.Fprintf(&b, "http_access allow %s CONNECT SSL_ports %s_dst\n", id, id)
	}
	return b.String()
}

func renderBlocky(ms []*Manifest, upstreams []string) string {
	var b strings.Builder
	b.WriteString("# Generado por `guardianctl policy render egress`. No editar a mano.\n")
	b.WriteString("upstreams:\n  groups:\n    default:\n")
	for _, u := range upstreams {
		fmt.Fprintf(&b, "      - %s\n", u)
	}
	b.WriteString("ports:\n  dns: 53\n  http: 4000\nlog:\n  level: info\n  format: text\n")
	// Auditoría (Fase 3): una línea por consulta con cliente, dominio y resultado (BLOCKED/RESOLVED).
	b.WriteString("queryLog:\n  type: console\n  logRetentionDays: 0\n")
	b.WriteString("blocking:\n  blockType: nxDomain\n  blockTTL: 1m\n  denylists:\n    all:\n      - |\n        /.*/\n")
	for _, m := range ms {
		if len(m.Allow) > 0 {
			fmt.Fprintf(&b, "    agent-%s:\n      - |\n        /.*/\n", m.Name)
		}
	}
	if len(ms) > 0 {
		b.WriteString("  allowlists:\n")
		for _, m := range ms {
			if len(m.Allow) == 0 {
				continue
			}
			fmt.Fprintf(&b, "    agent-%s:\n      - |\n", m.Name)
			for _, d := range m.Allow {
				if strings.HasPrefix(d, "*.") {
					// Blocky: una entrada de dominio cubre también sus subdominios.
					fmt.Fprintf(&b, "        %s\n", strings.TrimPrefix(d, "*."))
				} else {
					fmt.Fprintf(&b, "        /^%s$/\n", strings.ReplaceAll(d, ".", `\.`))
				}
			}
		}
	}
	b.WriteString("  clientGroupsBlock:\n    default:\n      - all\n")
	for _, m := range ms {
		fmt.Fprintf(&b, "    %s:\n      - all\n", m.IP)
		if len(m.Allow) > 0 {
			// Grupo agent-x: deniega todo y permite su allowlist (las allowlists ganan dentro
			// de los grupos asignados al cliente).
			fmt.Fprintf(&b, "      - agent-%s\n", m.Name)
		}
	}
	return b.String()
}

func cmdPolicy(args []string) int {
	if len(args) < 2 || args[0] != "render" {
		fmt.Fprintln(os.Stderr, "uso: guardianctl policy render egress|nftables|fortios|opnsense|unifi [--out archivo]")
		return 2
	}
	root := repoRoot()
	switch args[1] {
	case "egress":
		upstreams := []string{"1.1.1.1", "9.9.9.9"}
		for i := 2; i < len(args); i++ {
			if args[i] == "--upstreams" && i+1 < len(args) {
				upstreams = strings.Split(args[i+1], ",")
				i++
			}
		}
		ms, err := loadAllManifests(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "policy render egress:", err)
			return 1
		}
		squid := filepath.Join(root, "compose", "squid", "agents.conf")
		blocky := filepath.Join(root, "compose", "blocky", "config.yml")
		if err := os.WriteFile(squid, []byte(renderSquid(ms)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(blocky, []byte(renderBlocky(ms, upstreams)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("%-16s %-16s %s\n", "AGENTE", "IP", "EGRESS.ALLOW")
		for _, m := range ms {
			fmt.Printf("%-16s %-16s %s\n", m.Name, m.IP, strings.Join(m.Allow, ","))
		}
		fmt.Printf("\nescritos %s y %s\naplica con: make restart-egress\n", squid, blocky)
		return 0
	case "nftables", "fortios", "opnsense", "unifi":
		return cmdPolicyRender(root, args[1], args[2:])
	default:
		fmt.Fprintln(os.Stderr, "policy render: objetivos: egress | nftables | fortios | opnsense | unifi [--out archivo]")
		return 2
	}
}
