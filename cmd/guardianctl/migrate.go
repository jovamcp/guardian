// guardianctl migrate — migraciones entre versiones (internal/migrate). Las ejecuta upgrade;
// a mano sirven para instalaciones actualizadas con git o para reanudar una que falló.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jovamcp/guardian/internal/migrate"
	"github.com/jovamcp/guardian/internal/semver"
)

func cmdMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	from := fs.String("from", "", "versión desde la que migrar (por defecto, la registrada en "+migrate.StateFile+")")
	dryRun := fs.Bool("dry-run", false, "listar las migraciones pendientes sin aplicarlas")
	mark := fs.Bool("mark", false, "registrar la versión instalada como migrada sin ejecutar nada")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root := repoRoot()
	to := installedVersion(root)
	if *mark {
		if os.Geteuid() != 0 {
			fmt.Fprintln(os.Stderr, "migrate --mark: requiere root")
			return 1
		}
		if err := migrate.WriteState(root, to); err != nil {
			fmt.Fprintln(os.Stderr, "migrate:", err)
			return 1
		}
		fmt.Printf("registrado: migraciones al día hasta %s.\n", to)
		return 0
	}
	src := *from
	if src == "" {
		src = migrationsFrom(root)
	}
	if src == "" {
		fmt.Fprintln(os.Stderr, "migrate: no hay registro de migraciones; indica la versión de origen: guardianctl migrate --from 0.2.0 (o --mark si la instalación ya está al día)")
		return 1
	}
	pending, err := migrate.Pending(src, to)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}
	if *dryRun {
		fmt.Printf("de %s a %s: %d migraciones pendientes\n", src, to, len(pending))
		for _, m := range pending {
			fmt.Printf("  %s  %s\n", m.Version, m.Name)
		}
		return 0
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "migrate: requiere root (o --dry-run)")
		return 1
	}
	applied, err := migrate.Run(root, src, to, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v (aplicadas: %s)\n", err, strings.Join(applied, ", "))
		return 1
	}
	fmt.Printf("migraciones al día hasta %s (%d aplicadas).\n", to, len(applied))
	return 0
}

// migrationsFrom decide desde qué versión migrar: el registro compose/.migrated; si no existe,
// la versión anterior guardada por upgrade en .previous/META; si tampoco, "" (desconocida).
func migrationsFrom(root string) string {
	if s := migrate.State(root); s != "" {
		return s
	}
	raw, err := os.ReadFile(filepath.Join(root, previousDir, "META"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "version="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// runMigrations: lo llama upgrade tras copiar el árbol nuevo. Sin registro ni .previous no
// hay nada que migrar (instalación nueva): se registra la versión.
func runMigrations(root, newVersion string) error {
	from := migrationsFrom(root)
	if from == "" {
		return migrate.WriteState(root, newVersion)
	}
	applied, err := migrate.Run(root, from, newVersion, os.Stdout)
	if err != nil {
		return fmt.Errorf("%v (aplicadas: %s); corrige y ejecuta: sudo guardianctl migrate", err, strings.Join(applied, ", "))
	}
	if len(applied) > 0 {
		fmt.Printf("migraciones aplicadas: %s.\n", strings.Join(applied, ", "))
	}
	return nil
}

// checkMigrations (doctor): el registro de migraciones no puede ir por detrás de VERSION.
func checkMigrations(root string) error {
	cur := installedVersion(root)
	state := migrate.State(root)
	if state == "" {
		return errWarn{"sin registro de migraciones (" + migrate.StateFile + "): sudo guardianctl migrate --from <versión anterior>, o --mark si la instalación está al día"}
	}
	if semver.Less(state, cur) {
		pending, _ := migrate.Pending(state, cur)
		if len(pending) > 0 {
			return fmt.Errorf("migraciones pendientes de %s a %s (%d): sudo guardianctl migrate", state, cur, len(pending))
		}
		return errWarn{fmt.Sprintf("registro de migraciones en %s y VERSION en %s: sudo guardianctl migrate --mark", state, cur)}
	}
	return nil
}
