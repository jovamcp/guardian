package migrate

import (
	"os"
	"os/exec"
	"strings"
)

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// dockerVolumes lista los volúmenes cuyo nombre contiene filter; sin Docker devuelve nil.
func dockerVolumes(filter string) []string {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil
	}
	out, err := exec.Command("docker", "volume", "ls", "-q", "--filter", "name="+filter).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}
