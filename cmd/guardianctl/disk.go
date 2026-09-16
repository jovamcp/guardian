package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// diskUsage devuelve el porcentaje de uso del sistema de archivos que contiene path.
func diskUsage(path string) (percent float64, freeGB float64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	total := float64(st.Blocks) * float64(st.Bsize)
	avail := float64(st.Bavail) * float64(st.Bsize)
	if total == 0 {
		return 0, 0, fmt.Errorf("statfs %s: tamaño 0", path)
	}
	return (1 - avail/total) * 100, avail / 1e9, nil
}

// checkDiskSpace (doctor): el disco de Docker por debajo del 90 %. Loki deja de ingerir logs
// (WAL en throttling, "Ingester is shutting down") al superar el 90 % y la auditoría se detiene
// en silencio; visto en la prueba en caliente de v0.4 al descargar dos imágenes de agentes.
func checkDiskSpace(string) error {
	path := "/var/lib/docker"
	if out, err := run("docker", "info", "--format", "{{.DockerRootDir}}"); err == nil && strings.TrimSpace(out) != "" {
		path = strings.TrimSpace(out)
	}
	if _, err := os.Stat(path); err != nil {
		path = "/"
	}
	return diskSpaceVerdict(path, diskUsage)
}

func diskSpaceVerdict(path string, usage func(string) (float64, float64, error)) error {
	pct, free, err := usage(path)
	if err != nil {
		return errWarn{fmt.Sprintf("no se pudo medir el disco de %s: %v", path, err)}
	}
	switch {
	case pct >= 90:
		return fmt.Errorf("%s al %.0f%% (%.1f GB libres): Loki deja de ingerir logs por encima del 90%%. Libera espacio (docker image prune, docker builder prune, volúmenes huérfanos)", path, pct, free)
	case pct >= 80:
		return errWarn{fmt.Sprintf("%s al %.0f%% (%.1f GB libres); al 90%% Loki deja de ingerir logs", path, pct, free)}
	}
	return nil
}
