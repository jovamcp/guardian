// Package semver compara versiones X.Y.Z[-pre] (con o sin "v"). Solo lo que Guardian necesita.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// Parse devuelve [major, minor, patch] y el sufijo de prerelease ("" si no hay).
func Parse(v string) ([3]int, string, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	core, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return [3]int{}, "", fmt.Errorf("versión no válida: %q", v)
	}
	var n [3]int
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return [3]int{}, "", fmt.Errorf("versión no válida: %q", v)
		}
		n[i] = x
	}
	return n, pre, nil
}

// Compare devuelve -1, 0 o 1. Una prerelease es menor que la versión final ("0.3.0-dev" < "0.3.0").
func Compare(a, b string) (int, error) {
	na, pa, err := Parse(a)
	if err != nil {
		return 0, err
	}
	nb, pb, err := Parse(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < 3; i++ {
		if na[i] != nb[i] {
			if na[i] < nb[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	switch {
	case pa == pb:
		return 0, nil
	case pa == "":
		return 1, nil
	case pb == "":
		return -1, nil
	}
	return strings.Compare(pa, pb), nil
}

// Less es Compare(a, b) < 0; con versiones no válidas devuelve false.
func Less(a, b string) bool {
	c, err := Compare(a, b)
	return err == nil && c < 0
}
