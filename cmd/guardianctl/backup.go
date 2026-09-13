// guardianctl backup: copias de seguridad con restic (binario del sistema).
// Copia guardian.yaml, compose/.env, vault/, compose/certs/, agents/, un pg_dump de LiteLLM y
// los volúmenes Docker (tar desde un contenedor efímero de solo lectura). La contraseña del
// repositorio vive en el vault y llega a restic por RESTIC_PASSWORD_COMMAND (nunca en disco).
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	backupStage = "/var/tmp/guardian-backup"
	backupState = "/var/lib/guardian/last-backup"
	backupTag   = "guardian"
)

// Volúmenes por servicio; ollama_data solo con include_models.
var backupVolumes = []string{"caddy_data", "caddy_config", "pocket_id_data", "open_webui_data",
	"wg_easy_data", "grafana_data", "loki_data", "vector_data", "gateway_data"}

func resticEnv(root string, c Config) ([]string, error) {
	if c.BackupRepo == "" {
		return nil, errors.New("backup.repository vacío en guardian.yaml: copias desactivadas")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	ref := strings.TrimPrefix(c.BackupPassword, "vault:")
	if _, err := vaultRead(root, ref); err != nil {
		return nil, fmt.Errorf("contraseña del repositorio: %v", err)
	}
	env := append(os.Environ(),
		"RESTIC_REPOSITORY="+c.BackupRepo,
		"RESTIC_PASSWORD_COMMAND="+exe+" secret get "+ref,
		"RESTIC_CACHE_DIR=/var/cache/guardian-restic",
	)
	return env, nil
}

func restic(env []string, args ...string) error {
	cmd := exec.Command("restic", args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func requireRestic() error {
	if _, err := exec.LookPath("restic"); err != nil {
		return errors.New("falta restic (apt install restic)")
	}
	return nil
}

func cmdBackup(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "uso: guardianctl backup init | run | list | restore [snapshot] --to <dir> | schedule apply|remove")
		return 2
	}
	root := repoRoot()
	if args[0] == "schedule" {
		return cmdBackupSchedule(root, args[1:])
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "backup: requiere root (lee el vault y los volúmenes de Docker)")
		return 1
	}
	if err := requireRestic(); err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err)
		return 1
	}
	c, err := loadConfig(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err)
		return 1
	}
	env, err := resticEnv(root, c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err)
		return 1
	}
	switch args[0] {
	case "init":
		if strings.HasPrefix(c.BackupRepo, "/") {
			os.MkdirAll(c.BackupRepo, 0o700)
		}
		if err := restic(env, "init"); err != nil {
			fmt.Fprintln(os.Stderr, "backup init:", err, "(si el repositorio ya existe, sigue con `backup run`)")
			return 1
		}
		return 0
	case "run":
		return backupRun(root, c, env)
	case "list":
		if err := restic(env, "snapshots", "--tag", backupTag, "--compact"); err != nil {
			return 1
		}
		return 0
	case "restore":
		snap := "latest"
		to := ""
		for i := 1; i < len(args); i++ {
			if args[i] == "--to" && i+1 < len(args) {
				to = args[i+1]
				i++
			} else if !strings.HasPrefix(args[i], "-") {
				snap = args[i]
			}
		}
		if to == "" {
			fmt.Fprintln(os.Stderr, "backup restore: indica --to <directorio vacío>")
			return 2
		}
		if err := os.MkdirAll(to, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := restic(env, "restore", snap, "--tag", backupTag, "--target", to); err != nil {
			return 1
		}
		fmt.Printf(`
Recuperado en %s. Para volver a un host:
  1. Copia %s/stage/guardian.yaml y compose/.env al repo y vault/ y compose/certs/ a su sitio.
  2. Con la plataforma parada (sudo make down):
       for v in volumes/*.tgz: docker run --rm -v guardian_<vol>:/dst -v %s/stage/volumes:/src busybox tar xzf /src/<vol>.tgz -C /dst
  3. Las llaves del gateway viajan en volumes/gateway_data.tgz (paso 2).
  4. sudo make up && sudo make doctor
`, to, to, to)
		return 0
	default:
		fmt.Fprintln(os.Stderr, "backup: subcomando desconocido:", args[0])
		return 2
	}
}

func backupRun(root string, c Config, env []string) int {
	start := time.Now()
	stage := filepath.Join(backupStage, "stage")
	os.RemoveAll(backupStage)
	if err := os.MkdirAll(filepath.Join(stage, "volumes"), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(backupStage)

	// 1. Archivos de configuración y secretos (cifrados en el vault; .env en claro → el repo restic va cifrado).
	for _, rel := range []string{"guardian.yaml", "compose/.env", "compose/certs", "vault", "agents", "VERSION"} {
		src := filepath.Join(root, rel)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(stage, rel)
		os.MkdirAll(filepath.Dir(dst), 0o700)
		if out, err := exec.Command("cp", "-a", src, dst).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "copiando %s: %s\n", rel, out)
			return 1
		}
	}
	// 2. (instalaciones anteriores a v0.3) pg_dump de LiteLLM si el contenedor aún existe.
	if _, err := run("docker", "inspect", "gd-litellm-db"); err == nil {
		dump, err := run("docker", "exec", "gd-litellm-db", "pg_dump", "-U", "litellm", "--no-owner", "litellm")
		if err != nil {
			fmt.Fprintln(os.Stderr, "pg_dump de litellm-db:", err)
			return 1
		}
		gz := exec.Command("gzip", "-c")
		gz.Stdin = strings.NewReader(dump)
		f, _ := os.Create(filepath.Join(stage, "litellm.sql.gz"))
		gz.Stdout = f
		if err := gz.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "gzip:", err)
			return 1
		}
		f.Close()
	}
	// 3. Volúmenes Docker como tar (solo lectura, contenedor efímero sin red).
	vols := backupVolumes
	if c.BackupModels {
		vols = append(vols, "ollama_data")
	}
	for _, v := range vols {
		full := "guardian_" + v
		if _, err := run("docker", "volume", "inspect", full); err != nil {
			continue
		}
		// Solo lectura sobre el volumen; DAC_READ_SEARCH para leer directorios de otros uids
		// (p. ej. uploads de Pocket ID con modo 700).
		if _, err := run("docker", "run", "--rm", "--network", "none", "--cap-drop", "ALL", "--cap-add", "DAC_READ_SEARCH",
			"-v", full+":/src:ro", "-v", filepath.Join(stage, "volumes")+":/dst", probeImage,
			"tar", "czf", "/dst/"+v+".tgz", "-C", "/src", "."); err != nil {
			fmt.Fprintf(os.Stderr, "volumen %s: %v\n", v, err)
			return 1
		}
	}
	// 4. restic backup + retención.
	if err := restic(env, "backup", "--tag", backupTag, "--host", "guardian", stage); err != nil {
		return 1
	}
	if err := restic(env, "forget", "--tag", backupTag, "--prune", "--quiet",
		"--keep-daily", fmt.Sprint(c.KeepDaily), "--keep-weekly", fmt.Sprint(c.KeepWeekly), "--keep-monthly", fmt.Sprint(c.KeepMonthly)); err != nil {
		fmt.Fprintln(os.Stderr, "aviso: forget/prune falló:", err)
	}
	os.MkdirAll(filepath.Dir(backupState), 0o755)
	os.WriteFile(backupState, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
	fmt.Printf("copia completada en %s (%d volúmenes, pg_dump, config y vault)\n", time.Since(start).Round(time.Second), len(vols))
	return 0
}

func cmdBackupSchedule(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "uso: guardianctl backup schedule apply | remove")
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "backup schedule: requiere root")
		return 1
	}
	const sName, tName = "guardian-backup.service", "guardian-backup.timer"
	switch args[0] {
	case "apply":
		c, err := loadConfig(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if c.BackupRepo == "" {
			fmt.Fprintln(os.Stderr, "backup.repository vacío: nada que planificar")
			return 1
		}
		oc, err := cronToOnCalendar(c.BackupCron)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		exe, _ := os.Executable()
		if err := checkRootOwned(root, exe); err != nil {
			fmt.Fprintln(os.Stderr, "backup schedule apply:", err)
			return 1
		}
		service := fmt.Sprintf("# Generado por guardianctl backup schedule.\n[Unit]\nDescription=Copia de seguridad de Guardian (restic)\nAfter=docker.service\nRequires=docker.service\n\n[Service]\nType=oneshot\nWorkingDirectory=%s\nExecStart=%s backup run\nTimeoutStartSec=4h\nNice=10\nIOSchedulingClass=idle\n", root, exe)
		timer := fmt.Sprintf("# Generado por guardianctl backup schedule.\n[Unit]\nDescription=Planificación de copias de Guardian (cron: %s)\n\n[Timer]\nOnCalendar=%s\nPersistent=true\nRandomizedDelaySec=15m\nUnit=%s\n\n[Install]\nWantedBy=timers.target\n", c.BackupCron, oc, sName)
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
		fmt.Printf("timer de copias habilitado: cron %q → OnCalendar=%s\n", c.BackupCron, oc)
		return 0
	case "remove":
		exec.Command("systemctl", "disable", "--now", tName).Run()
		os.Remove(filepath.Join(unitDir, tName))
		os.Remove(filepath.Join(unitDir, sName))
		exec.Command("systemctl", "daemon-reload").Run()
		fmt.Println("timer de copias eliminado")
		return 0
	default:
		return 2
	}
}

// checkBackupFresh: WARN si no hay copias configuradas o la última tiene más de 2 días.
func checkBackupFresh(root string) error {
	c, err := loadConfig(root)
	if err != nil || c.BackupRepo == "" {
		return errWarn{"copias de seguridad no configuradas (backup.repository en guardian.yaml)"}
	}
	raw, err := os.ReadFile(backupState)
	if err != nil {
		return errWarn{"aún no se ha hecho ninguna copia: sudo ./bin/guardianctl backup init && backup run"}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(raw)))
	if err != nil {
		return errWarn{"marca de última copia ilegible"}
	}
	if age := time.Since(t); age > 48*time.Hour {
		return errWarn{fmt.Sprintf("la última copia tiene %s; revisa el timer guardian-backup", age.Round(time.Hour))}
	}
	return nil
}
