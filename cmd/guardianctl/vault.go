// Vault de secretos: archivos cifrados con age en vault/ (ignorado por git).
// Usa el binario `age` del sistema (apt install age); no se reimplementa criptografía.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var secretRefRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_./-]{0,127}$`)

func vaultDir(root string) string       { return filepath.Join(root, "vault") }
func vaultKeyPath(root string) string   { return filepath.Join(vaultDir(root), "key.txt") }
func vaultRecipPath(root string) string { return filepath.Join(vaultDir(root), "recipients.txt") }

func vaultSecretPath(root, ref string) (string, error) {
	if !secretRefRe.MatchString(ref) || strings.Contains(ref, "..") {
		return "", fmt.Errorf("referencia de secreto inválida %q (minúsculas, dígitos, _ . / -)", ref)
	}
	return filepath.Join(vaultDir(root), "secrets", ref+".age"), nil
}

func requireAge() error {
	if _, err := exec.LookPath("age"); err != nil {
		return errors.New("falta el binario age (apt install age)")
	}
	return nil
}

func vaultInit(root string) error {
	if err := requireAge(); err != nil {
		return err
	}
	if _, err := os.Stat(vaultKeyPath(root)); err == nil {
		return fmt.Errorf("%s ya existe; no se sobrescribe", vaultKeyPath(root))
	}
	if err := os.MkdirAll(filepath.Join(vaultDir(root), "secrets"), 0o700); err != nil {
		return err
	}
	keygen, err := exec.LookPath("age-keygen")
	if err != nil {
		return errors.New("falta age-keygen (viene con el paquete age)")
	}
	out, err := exec.Command(keygen, "-o", vaultKeyPath(root)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("age-keygen: %v: %s", err, out)
	}
	os.Chmod(vaultKeyPath(root), 0o600)
	// age-keygen imprime "Public key: age1…" por stderr.
	pub := ""
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "Public key: ") {
			pub = strings.TrimSpace(strings.TrimPrefix(l, "Public key: "))
		}
	}
	if pub == "" {
		return errors.New("no se pudo leer la clave pública de age-keygen")
	}
	if err := os.WriteFile(vaultRecipPath(root), []byte(pub+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("vault creado en %s\n  identidad: %s (0600, haz copia de seguridad fuera del host)\n  destinatario: %s\n", vaultDir(root), vaultKeyPath(root), pub)
	return nil
}

func vaultSet(root, ref string, value []byte) error {
	if err := requireAge(); err != nil {
		return err
	}
	if _, err := os.Stat(vaultRecipPath(root)); err != nil {
		return errors.New("vault no inicializado: ejecuta `guardianctl secret init`")
	}
	p, err := vaultSecretPath(root, ref)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	cmd := exec.Command("age", "-R", vaultRecipPath(root), "-o", p)
	cmd.Stdin = bytes.NewReader(value)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("age: %v: %s", err, out)
	}
	return os.Chmod(p, 0o600)
}

func vaultRead(root, ref string) (string, error) {
	if err := requireAge(); err != nil {
		return "", err
	}
	p, err := vaultSecretPath(root, ref)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("no existe el secreto vault:%s (créalo con `guardianctl secret set %s`)", ref, ref)
	}
	cmd := exec.Command("age", "-d", "-i", vaultKeyPath(root), p)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("age -d %s: %v: %s", ref, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func vaultList(root string) ([]string, error) {
	base := filepath.Join(vaultDir(root), "secrets")
	var refs []string
	filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".age") {
			rel, _ := filepath.Rel(base, p)
			refs = append(refs, strings.TrimSuffix(rel, ".age"))
		}
		return nil
	})
	sort.Strings(refs)
	return refs, nil
}

func cmdSecret(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "uso: guardianctl secret init | set <ref> [--from-file f] | get <ref> | list | rm <ref>")
		return 2
	}
	root := repoRoot()
	switch args[0] {
	case "init":
		if err := vaultInit(root); err != nil {
			fmt.Fprintln(os.Stderr, "secret init:", err)
			return 1
		}
		return 0
	case "set":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "uso: guardianctl secret set <ref> [--from-file archivo]   (o el valor por stdin)")
			return 2
		}
		var value []byte
		var err error
		if len(args) >= 4 && args[2] == "--from-file" {
			value, err = os.ReadFile(args[3])
		} else {
			if isTerminal(os.Stdin) {
				fmt.Fprintf(os.Stderr, "valor para %s (no se muestra, termina con Enter): ", args[1])
			}
			value, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "secret set:", err)
			return 1
		}
		value = bytes.TrimRight(value, "\r\n")
		if len(value) == 0 {
			fmt.Fprintln(os.Stderr, "secret set: valor vacío")
			return 1
		}
		if err := vaultSet(root, args[1], value); err != nil {
			fmt.Fprintln(os.Stderr, "secret set:", err)
			return 1
		}
		fmt.Printf("guardado vault:%s\n", args[1])
		return 0
	case "get":
		if len(args) < 2 {
			return 2
		}
		v, err := vaultRead(root, args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "secret get:", err)
			return 1
		}
		fmt.Println(v)
		return 0
	case "list":
		refs, _ := vaultList(root)
		for _, r := range refs {
			fmt.Println(r)
		}
		return 0
	case "rm":
		if len(args) < 2 {
			return 2
		}
		p, err := vaultSecretPath(root, args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "secret rm:", err)
			return 1
		}
		if err := os.Remove(p); err != nil {
			fmt.Fprintln(os.Stderr, "secret rm:", err)
			return 1
		}
		fmt.Printf("eliminado vault:%s\n", args[1])
		return 0
	default:
		fmt.Fprintln(os.Stderr, "secret: subcomando desconocido:", args[0])
		return 2
	}
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
