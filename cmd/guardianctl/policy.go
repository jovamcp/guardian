// policy render: nftables (define), fortios (CLI) y opnsense (guía) desde guardian.yaml.
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

type fwVars struct {
	VLAN            int
	GatewayIP       string
	ParentInterface string
	LANInterface    string
	WANInterface    string
	LANCIDR         string
	AICIDR          string
	HostIP          string
	WANIP           string
	Domain          string
	WGCIDR          string
	WGEndpoint      string
}

func firewallVars(c Config) fwVars {
	return fwVars{VLAN: c.AIVLAN, GatewayIP: c.AIGatewayIP, ParentInterface: c.FWParent,
		LANInterface: c.FWLAN, WANInterface: c.FWWAN, LANCIDR: c.LANCIDR, AICIDR: c.AICIDR,
		HostIP: c.AIHostIP, WANIP: c.FWWANIP, Domain: c.Domain, WGCIDR: c.RemoteCIDR, WGEndpoint: c.RemoteEndp}
}

func renderTemplate(root, rel string, vars fwVars) (string, error) {
	p := filepath.Join(root, rel)
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	funcs := template.FuncMap{"cidrBits": func(cidr string) string {
		if i := strings.LastIndex(cidr, "/"); i >= 0 {
			return cidr[i+1:]
		}
		return "24"
	}}
	t, err := template.New(filepath.Base(p)).Funcs(funcs).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return "", fmt.Errorf("%s: %v", rel, err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, vars); err != nil {
		return "", fmt.Errorf("%s: %v", rel, err)
	}
	return b.String(), nil
}

func cmdPolicyRender(root, target string, args []string) int {
	out := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--out" && i+1 < len(args) {
			out = args[i+1]
			i++
		}
	}
	emit := func(s string) int {
		if out == "" {
			fmt.Print(s)
			return 0
		}
		if err := os.WriteFile(out, []byte(s), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "escrito %s\n", out)
		return 0
	}
	c, err := loadConfig(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy render %s: %v (ejecuta `guardianctl init`)\n", target, err)
		return 1
	}
	switch target {
	case "nftables":
		changed, err := renderNftables(root, c)
		if err != nil {
			fmt.Fprintln(os.Stderr, "policy render nftables:", err)
			return 1
		}
		if len(changed) == 0 {
			fmt.Println("nftables: los define ya coinciden con guardian.yaml")
		} else {
			fmt.Printf("nftables: actualizados %s\n", strings.Join(changed, ", "))
		}
		fmt.Printf("  LAN_NET=%s  AI_NET=%s  WG_NET=%s\n  valida con: make nft-check   aplica con: make nft-apply (o sudo ./install.sh --with-nftables)\n",
			c.LANCIDR, c.AICIDR, c.RemoteCIDR)
		return 0
	case "fortios":
		s, err := renderTemplate(root, "policies/fortios/ai-zone.tmpl", firewallVars(c))
		if err != nil {
			fmt.Fprintln(os.Stderr, "policy render fortios:", err)
			return 1
		}
		if c.FWWANIP == "" || c.FWWANIP == "0.0.0.0" {
			fmt.Fprintln(os.Stderr, "aviso: firewall.wan_ip no está definida en guardian.yaml; la VIP de WireGuard sale con 0.0.0.0")
		}
		return emit(s)
	case "opnsense", "unifi":
		s, err := renderTemplate(root, "policies/"+target+"/ai-zone.md.tmpl", firewallVars(c))
		if err != nil {
			fmt.Fprintf(os.Stderr, "policy render %s: %v\n", target, err)
			return 1
		}
		return emit(s)
	default:
		fmt.Fprintln(os.Stderr, "policy render: objetivos: egress | nftables | fortios | opnsense | unifi")
		return 2
	}
}
