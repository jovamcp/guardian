// guardianctl agent schedule: convierte schedule.cron de los manifiestos en timers de systemd
// que ejecutan sandbox/runner.sh. El planificador es el host (sin contenedor con socket).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const unitDir = "/etc/systemd/system"

// cronToOnCalendar convierte una expresión cron de 5 campos al formato OnCalendar de systemd.
// Soporta: *, valores, listas (a,b), rangos (a-b) y pasos (*/n, a-b/n) en cada campo.
// Ejemplos: "*/15 * * * *" → "*-*-* *:00/15:00"; "0 3 * * 1" → "Mon *-*-* 03:00:00".
func cronToOnCalendar(expr string) (string, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return "", fmt.Errorf("cron %q: se esperan 5 campos (min hora día mes díasemana)", expr)
	}
	min, hour, dom, mon, dow := f[0], f[1], f[2], f[3], f[4]
	conv := func(field string, lo, hi int) (string, error) {
		if field == "*" {
			return "*", nil
		}
		var parts []string
		for _, p := range strings.Split(field, ",") {
			base, step := p, ""
			if i := strings.Index(p, "/"); i >= 0 {
				base, step = p[:i], p[i+1:]
			}
			rng := base
			if base == "*" {
				rng = fmt.Sprintf("%d..%d", lo, hi)
			} else if strings.Contains(base, "-") {
				ab := strings.SplitN(base, "-", 2)
				rng = ab[0] + ".." + ab[1]
			}
			for _, n := range strings.FieldsFunc(rng, func(r rune) bool { return r == '.' }) {
				if v, err := strconv.Atoi(n); err != nil || v < lo || v > hi {
					return "", fmt.Errorf("cron %q: valor %q fuera de %d-%d", expr, n, lo, hi)
				}
			}
			if step != "" {
				if _, err := strconv.Atoi(step); err != nil {
					return "", fmt.Errorf("cron %q: paso %q inválido", expr, step)
				}
				rng += "/" + step
			}
			parts = append(parts, rng)
		}
		return strings.Join(parts, ","), nil
	}
	m, err := conv(min, 0, 59)
	if err != nil {
		return "", err
	}
	h, err := conv(hour, 0, 23)
	if err != nil {
		return "", err
	}
	d, err := conv(dom, 1, 31)
	if err != nil {
		return "", err
	}
	mo, err := conv(mon, 1, 12)
	if err != nil {
		return "", err
	}
	// systemd usa dos dígitos para pasos desde cero: "*/15" → "00/15".
	fix := func(s string) string {
		if strings.HasPrefix(s, "0..59/") {
			return "00/" + strings.TrimPrefix(s, "0..59/")
		}
		if strings.HasPrefix(s, "0..23/") {
			return "00/" + strings.TrimPrefix(s, "0..23/")
		}
		return s
	}
	m, h = pad2(fix(m)), pad2(fix(h))
	dows := ""
	if dow != "*" {
		names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
		var ds []string
		for _, p := range strings.Split(dow, ",") {
			if strings.Contains(p, "-") {
				ab := strings.SplitN(p, "-", 2)
				a, err1 := strconv.Atoi(ab[0])
				b, err2 := strconv.Atoi(ab[1])
				if err1 != nil || err2 != nil || a < 0 || b > 7 || a > b {
					return "", fmt.Errorf("cron %q: día de la semana %q inválido", expr, p)
				}
				ds = append(ds, names[a%7]+".."+names[b%7])
				continue
			}
			v, err := strconv.Atoi(p)
			if err != nil || v < 0 || v > 7 {
				return "", fmt.Errorf("cron %q: día de la semana %q inválido", expr, p)
			}
			ds = append(ds, names[v%7])
		}
		dows = strings.Join(ds, ",") + " "
	}
	return fmt.Sprintf("%s*-%s-%s %s:%s:00", dows, mo, d, h, m), nil
}

// pad2 rellena a dos dígitos los valores (no los pasos) de un campo de hora/minuto:
// "3" → "03", "1..5/2" → "01..05/2", "00/6" se mantiene.
func pad2(field string) string {
	if field == "*" {
		return field
	}
	var out []string
	for _, part := range strings.Split(field, ",") {
		val, step, hasStep := strings.Cut(part, "/")
		nums := strings.Split(val, "..")
		for i, n := range nums {
			if len(n) == 1 {
				nums[i] = "0" + n
			}
		}
		val = strings.Join(nums, "..")
		if hasStep {
			val += "/" + step
		}
		out = append(out, val)
	}
	return strings.Join(out, ",")
}

func unitNames(name string) (string, string) {
	return "guardian-agent-" + name + ".service", "guardian-agent-" + name + ".timer"
}

func renderUnits(root string, m *Manifest, onCalendar string) (string, string) {
	runner := filepath.Join(root, "sandbox", "runner.sh")
	service := fmt.Sprintf(`# Generado por guardianctl agent schedule. No editar a mano.
[Unit]
Description=Guardian agent %s (manifiesto %s)
After=docker.service network-online.target
Requires=docker.service

[Service]
Type=oneshot
WorkingDirectory=%s
ExecStart=%s %s
TimeoutStartSec=6h
# El sandbox real lo aplica docker run; aquí solo evitamos que el runner escale.
NoNewPrivileges=true
`, m.Name, m.Path, root, runner, m.Name)
	timer := fmt.Sprintf(`# Generado por guardianctl agent schedule. No editar a mano.
[Unit]
Description=Planificación del agente Guardian %s (cron: %s)

[Timer]
OnCalendar=%s
Persistent=true
RandomizedDelaySec=30
Unit=%s

[Install]
WantedBy=timers.target
`, m.Name, m.Cron, onCalendar, "guardian-agent-"+m.Name+".service")
	return service, timer
}

func cmdAgentSchedule(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "uso: guardianctl agent schedule apply | list | remove <nombre> | show <nombre>")
		return 2
	}
	root := repoRoot()
	switch args[0] {
	case "show":
		if len(args) < 2 {
			return 2
		}
		m, err := findManifest(root, args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if m.Cron == "" {
			fmt.Printf("%s no tiene schedule.cron\n", m.Name)
			return 0
		}
		oc, err := cronToOnCalendar(m.Cron)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		s, t := renderUnits(root, m, oc)
		fmt.Println(s)
		fmt.Println(t)
		return 0
	case "apply":
		if os.Geteuid() != 0 {
			fmt.Fprintln(os.Stderr, "agent schedule apply: requiere root")
			return 1
		}
		exe, _ := os.Executable()
		if err := checkRootOwned(root, filepath.Join(root, "sandbox", "runner.sh"), exe, filepath.Join(root, "agents")); err != nil {
			fmt.Fprintln(os.Stderr, "agent schedule apply:", err)
			return 1
		}
		ms, err := loadAllManifests(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		n := 0
		for _, m := range ms {
			if m.Cron == "" {
				continue
			}
			oc, err := cronToOnCalendar(m.Cron)
			if err != nil {
				fmt.Fprintln(os.Stderr, "agent schedule:", err)
				return 1
			}
			if out, err := exec.Command("systemd-analyze", "calendar", oc).CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "agent schedule: OnCalendar %q inválido: %s\n", oc, strings.TrimSpace(string(out)))
				return 1
			}
			sName, tName := unitNames(m.Name)
			s, t := renderUnits(root, m, oc)
			if err := os.WriteFile(filepath.Join(unitDir, sName), []byte(s), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			if err := os.WriteFile(filepath.Join(unitDir, tName), []byte(t), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			fmt.Printf("%-16s cron %-16q → OnCalendar=%s\n", m.Name, m.Cron, oc)
			n++
		}
		if n == 0 {
			fmt.Println("ningún manifiesto con schedule.cron")
			return 0
		}
		if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, "daemon-reload:", string(out))
			return 1
		}
		for _, m := range ms {
			if m.Cron == "" {
				continue
			}
			_, tName := unitNames(m.Name)
			if out, err := exec.Command("systemctl", "enable", "--now", tName).CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "enable %s: %s\n", tName, string(out))
				return 1
			}
		}
		fmt.Printf("%d timer(s) habilitado(s). Estado: systemctl list-timers 'guardian-agent-*'\n", n)
		return 0
	case "list":
		cmd := exec.Command("systemctl", "list-timers", "--all", "--no-pager", "guardian-agent-*")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.Run()
		return 0
	case "remove":
		if len(args) < 2 {
			return 2
		}
		if os.Geteuid() != 0 {
			fmt.Fprintln(os.Stderr, "agent schedule remove: requiere root")
			return 1
		}
		if !nameRe.MatchString(args[1]) {
			fmt.Fprintln(os.Stderr, "agent schedule remove: nombre de agente inválido")
			return 2
		}
		sName, tName := unitNames(args[1])
		exec.Command("systemctl", "disable", "--now", tName).Run()
		os.Remove(filepath.Join(unitDir, tName))
		os.Remove(filepath.Join(unitDir, sName))
		exec.Command("systemctl", "daemon-reload").Run()
		fmt.Printf("timer de %s eliminado\n", args[1])
		return 0
	default:
		fmt.Fprintln(os.Stderr, "agent schedule: subcomando desconocido:", args[0])
		return 2
	}
}
