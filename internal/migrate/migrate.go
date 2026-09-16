// Package migrate contiene las migraciones entre versiones de Guardian: pasos idempotentes
// que adaptan la instalación (compose/.env, guardian.yaml, archivos sobrantes) cuando se
// actualiza. Las ejecuta `guardianctl upgrade` tras copiar los archivos nuevos, o
// `guardianctl migrate` a mano. El estado (última versión migrada) vive en compose/.migrated,
// fuera de lo que el tarball sobrescribe. Nunca borran volúmenes: los listan.
package migrate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jovamcp/guardian/internal/semver"
)

// StateFile guarda la versión hasta la que se han aplicado migraciones (relativo al repo).
const StateFile = "compose/.migrated"

// Migration es un paso introducido por Version. Se aplica cuando la versión de origen es
// anterior a Version y la de destino igual o posterior. Debe ser idempotente.
type Migration struct {
	Version string
	Name    string
	Apply   func(root string, log io.Writer) error
}

// All, en orden de versión.
var All = []Migration{
	{Version: "0.3.0", Name: "litellm-to-gateway", Apply: liteLLMToGateway},
}

// State devuelve la última versión migrada registrada, o "" si no hay registro.
func State(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, StateFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// WriteState registra la versión hasta la que la instalación está migrada.
func WriteState(root, v string) error {
	p := filepath.Join(root, StateFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(strings.TrimSpace(v)+"\n"), 0o644)
}

// Pending devuelve las migraciones que hay que aplicar para pasar de from a to.
func Pending(from, to string) ([]Migration, error) {
	if _, _, err := semver.Parse(from); err != nil {
		return nil, fmt.Errorf("versión de origen: %v", err)
	}
	if _, _, err := semver.Parse(to); err != nil {
		return nil, fmt.Errorf("versión de destino: %v", err)
	}
	var out []Migration
	for _, m := range All {
		if semver.Less(from, m.Version) && !semver.Less(to, m.Version) {
			out = append(out, m)
		}
	}
	return out, nil
}

// Run aplica las migraciones pendientes de from a to, registrando el estado tras cada una
// y al final (to). Devuelve los nombres aplicados. Si una falla, el estado queda en la última
// que terminó bien.
func Run(root, from, to string, log io.Writer) ([]string, error) {
	pending, err := Pending(from, to)
	if err != nil {
		return nil, err
	}
	var applied []string
	for _, m := range pending {
		fmt.Fprintf(log, "migración %s (%s)…\n", m.Version, m.Name)
		if err := m.Apply(root, log); err != nil {
			return applied, fmt.Errorf("migración %s (%s): %w", m.Version, m.Name, err)
		}
		if err := WriteState(root, m.Version); err != nil {
			return applied, err
		}
		applied = append(applied, m.Name)
	}
	if err := WriteState(root, to); err != nil {
		return applied, err
	}
	return applied, nil
}

// ---------------------------------------------------------------- 0.3.0

// liteLLMToGateway: LITELLM_MASTER_KEY pasa a GATEWAY_MASTER_KEY si esta está vacía y aquella
// tiene el formato que exige gd-gateway; se retiran LITELLM_SALT_KEY y LITELLM_DB_PASSWORD;
// desaparece compose/litellm/ (solo configuración); los volúmenes de LiteLLM se listan, no se borran.
func liteLLMToGateway(root string, log io.Writer) error {
	envPath := filepath.Join(root, "compose", ".env")
	if raw, err := os.ReadFile(envPath); err == nil {
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		get := func(key string) (int, string) {
			for i, l := range lines {
				if k, v, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == key && !strings.HasPrefix(l, "#") {
					return i, strings.TrimSpace(v)
				}
			}
			return -1, ""
		}
		gi, gv := get("GATEWAY_MASTER_KEY")
		_, lv := get("LITELLM_MASTER_KEY")
		if gv == "" && strings.HasPrefix(lv, "sk-") && len(lv) >= 20 {
			if gi >= 0 {
				lines[gi] = "GATEWAY_MASTER_KEY=" + lv
			} else {
				lines = append(lines, "GATEWAY_MASTER_KEY="+lv)
			}
			fmt.Fprintln(log, "  GATEWAY_MASTER_KEY toma el valor de LITELLM_MASTER_KEY.")
		}
		var out []string
		removed := 0
		for _, l := range lines {
			k, _, ok := strings.Cut(l, "=")
			if ok && !strings.HasPrefix(l, "#") && strings.HasPrefix(strings.TrimSpace(k), "LITELLM_") {
				removed++
				continue
			}
			out = append(out, l)
		}
		if removed > 0 {
			fmt.Fprintf(log, "  retiradas %d variables LITELLM_* de compose/.env.\n", removed)
		}
		if err := os.WriteFile(envPath, []byte(strings.Join(out, "\n")+"\n"), 0o600); err != nil {
			return err
		}
	}
	if dir := filepath.Join(root, "compose", "litellm"); dirExists(dir) {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Fprintln(log, "  eliminado compose/litellm/ (configuración de LiteLLM).")
	}
	if vols := dockerVolumes("litellm"); len(vols) > 0 {
		fmt.Fprintf(log, "  volúmenes de LiteLLM que ya no se usan (bórralos cuando quieras): docker volume rm %s\n", strings.Join(vols, " "))
	}
	return nil
}
