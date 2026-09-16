// guardianctl upgrade — actualización a un release firmado (docs/prompts/07-v0.4.md).
//
// Flujo: consulta el release en GitHub → descarga tarball + SHA256SUMS (+ .sig/.pem) →
// verifica SHA-256 en Go y la firma con cosign si está instalado → copia previa con restic si
// hay repositorio configurado → guarda el árbol actual en .previous/ → extrae el tarball sobre
// el repo (sin tocar guardian.yaml, compose/.env, vault/, certs ni manifiestos propios) →
// binario nuevo → .env sincronizado → migraciones → compose up → doctor.
// Nada descargado se ejecuta ni se extrae antes de verificarlo. Nunca borra volúmenes.
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jovamcp/guardian/internal/semver"
)

const (
	// Repositorio de GitHub del que se descargan los releases (identidad que verifica cosign).
	upgradeRepo = "jovamcp/guardian"
	// Identidad OIDC del workflow que firma los artefactos (misma que install.sh).
	cosignIdentity = "^https://github.com/" + upgradeRepo + "/.github/workflows/release.yml@"
	cosignIssuer   = "https://token.actions.githubusercontent.com"
	previousDir    = ".previous"
	stagingDir     = ".upgrade"
	// Tamaño máximo de una entrada del tarball (los binarios de guardianctl rondan 9 MB).
	maxEntrySize = 64 << 20
)

// releasesAPI se sobrescribe en los tests (servidor httptest); env GUARDIAN_RELEASES_API para ensayos.
var releasesAPI = "https://api.github.com/repos/" + upgradeRepo + "/releases"

// Archivos del tarball que el usuario suele editar a mano: no se sobrescriben; la versión nueva
// se deja al lado como <archivo>.new y se avisa si difiere.
var protectedFiles = []string{
	"compose/gateway/config.yaml",
	"nftables/guardian.nft",
	"nftables/docker-user.nft",
}

// Lo que nunca sale del árbol actual (datos del usuario) ni entra en .previous/.
var userData = []string{"guardian.yaml", "vault", "compose/.env", "compose/certs", "compose/.extra-files", ".git", previousDir, stagingDir, "dist"}

type release struct {
	Tag    string `json:"tag_name"`
	Pre    bool   `json:"prerelease"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (r release) asset(name string) (string, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL, true
		}
	}
	return "", false
}

// upgradeHooks permite a los tests sustituir las acciones que tocan Docker o el host.
type upgradeHooks struct {
	backup  func(root string) error
	install func(root, newVersion string) error // binario, .env, migraciones
	stack   func(root string) error             // compose pull/up + caddy reload
	doctor  func(root string) int
	cosign  func(dir string) error // verificación de firma (nil = usar cosign real)
}

func defaultHooks() upgradeHooks {
	return upgradeHooks{backup: preUpgradeBackup, install: postSwapInstall, stack: restartStack, doctor: runNewDoctor, cosign: cosignVerify}
}

func cmdUpgrade(args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	check := fs.Bool("check", false, "solo comprobar si hay una versión nueva")
	to := fs.String("to", "", "versión concreta (vX.Y.Z); por defecto el último release")
	rollback := fs.Bool("rollback", false, "volver a la versión anterior guardada en "+previousDir+"/")
	yes := fs.Bool("yes", false, "no pedir confirmación")
	noBackup := fs.Bool("no-backup", false, "no hacer copia con restic antes de actualizar")
	requireSig := fs.Bool("require-signature", false, "fallar si cosign no está instalado (en vez de avisar)")
	dryRun := fs.Bool("dry-run", false, "descargar, verificar y mostrar qué cambiaría, sin tocar nada (no requiere root)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if v := os.Getenv("GUARDIAN_RELEASES_API"); v != "" {
		releasesAPI = v
	}
	root := repoRoot()
	opts := upgradeOpts{root: root, to: *to, yes: *yes, noBackup: *noBackup, requireSig: *requireSig, dryRun: *dryRun, out: os.Stdout, in: os.Stdin}
	if *rollback {
		if os.Geteuid() != 0 {
			fmt.Fprintln(os.Stderr, "upgrade --rollback: requiere root")
			return 1
		}
		return exit(doRollback(opts, defaultHooks()))
	}
	if *check {
		return exit(doCheck(opts))
	}
	if os.Geteuid() != 0 && !*dryRun {
		fmt.Fprintln(os.Stderr, "upgrade: requiere root (o --dry-run para ensayar)")
		return 1
	}
	return exit(doUpgrade(opts, defaultHooks()))
}

func exit(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, "upgrade:", err)
		return 1
	}
	return 0
}

type upgradeOpts struct {
	root       string
	to         string
	yes        bool
	noBackup   bool
	requireSig bool
	dryRun     bool
	out        io.Writer
	in         io.Reader
}

// installedVersion lee el archivo VERSION del repo (fuente de verdad de lo instalado).
func installedVersion(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return version
	}
	return strings.TrimSpace(string(raw))
}

func fetchRelease(tag string) (release, error) {
	url := releasesAPI + "/latest"
	if tag != "" {
		url = releasesAPI + "/tags/" + tag
	}
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "guardianctl/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return release{}, fmt.Errorf("no existe el release %s", tag)
	}
	if resp.StatusCode != 200 {
		return release{}, fmt.Errorf("GitHub respondió %s al consultar %s", resp.Status, url)
	}
	var r release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return release{}, fmt.Errorf("respuesta de GitHub ilegible: %v", err)
	}
	if _, _, err := semver.Parse(r.Tag); err != nil {
		return release{}, err
	}
	return r, nil
}

func doCheck(o upgradeOpts) error {
	cur := installedVersion(o.root)
	r, err := fetchRelease(o.to)
	if err != nil {
		return err
	}
	cmp, err := semver.Compare(cur, r.Tag)
	if err != nil {
		return err
	}
	fmt.Fprintf(o.out, "instalada: %s\nrelease:   %s\n", cur, r.Tag)
	switch {
	case cmp < 0:
		fmt.Fprintf(o.out, "hay una versión nueva: sudo guardianctl upgrade%s\n", toFlag(o.to))
	case cmp == 0:
		fmt.Fprintln(o.out, "al día.")
	default:
		fmt.Fprintln(o.out, "la versión instalada es más reciente que el release.")
	}
	return nil
}

func toFlag(to string) string {
	if to == "" {
		return ""
	}
	return " --to " + to
}

func doUpgrade(o upgradeOpts, h upgradeHooks) error {
	cur := installedVersion(o.root)
	r, err := fetchRelease(o.to)
	if err != nil {
		return err
	}
	target := strings.TrimPrefix(r.Tag, "v")
	cmp, err := semver.Compare(cur, r.Tag)
	if err != nil {
		return err
	}
	if cmp == 0 {
		fmt.Fprintf(o.out, "ya está instalada la %s; nada que hacer.\n", r.Tag)
		return nil
	}
	if cmp > 0 && o.to == "" {
		return fmt.Errorf("la versión instalada (%s) es más reciente que el último release (%s); usa --to vX.Y.Z para bajar a propósito", cur, r.Tag)
	}
	if r.Pre && o.to == "" {
		return fmt.Errorf("%s es una prerelease; para instalarla usa --to %s", r.Tag, r.Tag)
	}
	fmt.Fprintf(o.out, "actualización %s → %s\n", cur, r.Tag)
	if !o.yes && !o.dryRun && !confirm(o, "¿continuar? [s/N] ") {
		return errors.New("cancelado")
	}

	// 1. Descarga y verificación (en .upgrade/, fuera del árbol activo).
	stage := filepath.Join(o.root, stagingDir, r.Tag)
	os.RemoveAll(filepath.Join(o.root, stagingDir))
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Join(o.root, stagingDir))
	tarName := "guardian-" + target + ".tar.gz"
	for _, name := range []string{tarName, "SHA256SUMS"} {
		url, ok := r.asset(name)
		if !ok {
			return fmt.Errorf("el release %s no tiene el artefacto %s", r.Tag, name)
		}
		if err := download(url, filepath.Join(stage, name)); err != nil {
			return fmt.Errorf("descarga de %s: %v", name, err)
		}
	}
	if err := verifySHA256(filepath.Join(stage, "SHA256SUMS"), filepath.Join(stage, tarName)); err != nil {
		return err
	}
	fmt.Fprintln(o.out, "SHA-256 del tarball verificado.")
	if err := verifySignature(o, r, stage, h); err != nil {
		return err
	}
	// Extraer a un directorio temporal para validar el contenido antes de tocar el repo.
	tree := filepath.Join(stage, "tree")
	files, err := extractTarball(filepath.Join(stage, tarName), tree, "guardian-"+target+"/")
	if err != nil {
		return fmt.Errorf("tarball: %v", err)
	}
	if got := installedVersion(tree); got != target {
		return fmt.Errorf("el tarball dice VERSION=%s y el release es %s", got, r.Tag)
	}
	if _, err := os.Stat(filepath.Join(tree, "compose", "docker-compose.yml")); err != nil {
		return errors.New("el tarball no contiene compose/docker-compose.yml")
	}
	fmt.Fprintf(o.out, "tarball extraído y validado (%d archivos).\n", len(files))
	if o.dryRun {
		return dryRunReport(o, tree, files)
	}

	// 2. Copia previa.
	if !o.noBackup {
		if err := h.backup(o.root); err != nil {
			return fmt.Errorf("copia previa: %v (usa --no-backup para omitirla a propósito)", err)
		}
	}

	// 3. Árbol anterior → .previous/ (para --rollback).
	if err := snapshotPrevious(o.root, cur, files); err != nil {
		return fmt.Errorf("guardar versión anterior: %v", err)
	}

	// 4. Intercambio de archivos.
	newFiles, err := applyTree(o.root, tree, files, o.out)
	if err != nil {
		return fmt.Errorf("aplicar archivos (el árbol anterior está en %s/): %v", previousDir, err)
	}
	fmt.Fprintf(o.out, "archivos actualizados: %d.\n", len(newFiles))

	// 5. Binario, .env, migraciones; plataforma; doctor.
	if err := h.install(o.root, target); err != nil {
		return err
	}
	if err := h.stack(o.root); err != nil {
		return fmt.Errorf("levantar la plataforma: %v (para volver: sudo guardianctl upgrade --rollback)", err)
	}
	code := h.doctor(o.root)
	if code != 0 {
		fmt.Fprintf(o.out, "doctor terminó con código %d: revisa los fallos; para volver: sudo guardianctl upgrade --rollback\n", code)
	}
	fmt.Fprintf(o.out, "Guardian actualizado a %s.\n", r.Tag)
	return nil
}

func confirm(o upgradeOpts, prompt string) bool {
	fmt.Fprint(o.out, prompt)
	line, _ := bufio.NewReader(o.in).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "s" || line == "si" || line == "sí" || line == "y" || line == "yes"
}

func download(url, dst string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "guardianctl/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s", resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 512<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// verifySHA256 comprueba que el archivo aparece en SHA256SUMS con la suma correcta.
func verifySHA256(sumsPath, filePath string) error {
	raw, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	name := filepath.Base(filePath)
	var expected string
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			expected = strings.ToLower(f[0])
		}
	}
	if len(expected) != 64 {
		return fmt.Errorf("%s no aparece en SHA256SUMS", name)
	}
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != expected {
		return fmt.Errorf("SHA-256 de %s no coincide (esperado %s, obtenido %s)", name, expected, got)
	}
	return nil
}

// verifySignature descarga SHA256SUMS.sig/.pem y verifica con cosign (si está). Sin cosign:
// aviso, o error con --require-signature. Misma política que install.sh.
func verifySignature(o upgradeOpts, r release, stage string, h upgradeHooks) error {
	verify := h.cosign
	if verify == nil {
		verify = cosignVerify
	}
	if _, err := exec.LookPath("cosign"); err != nil && h.cosign == nil {
		if o.requireSig {
			return errors.New("cosign no está instalado y se pidió --require-signature (apt install cosign)")
		}
		fmt.Fprintln(o.out, "AVISO: cosign no está instalado; solo se verifica SHA-256. Para verificar la firma: apt install cosign.")
		return nil
	}
	for _, name := range []string{"SHA256SUMS.sig", "SHA256SUMS.pem"} {
		url, ok := r.asset(name)
		if !ok {
			return fmt.Errorf("el release %s no tiene %s: no se puede verificar la firma", r.Tag, name)
		}
		if err := download(url, filepath.Join(stage, name)); err != nil {
			return fmt.Errorf("descarga de %s: %v", name, err)
		}
	}
	if err := verify(stage); err != nil {
		return fmt.Errorf("firma cosign de SHA256SUMS no válida: %v", err)
	}
	fmt.Fprintln(o.out, "firma cosign de SHA256SUMS verificada (workflow release.yml de "+upgradeRepo+").")
	return nil
}

func cosignVerify(dir string) error {
	_, err := run("cosign", "verify-blob",
		"--certificate", filepath.Join(dir, "SHA256SUMS.pem"),
		"--signature", filepath.Join(dir, "SHA256SUMS.sig"),
		"--certificate-identity-regexp", cosignIdentity,
		"--certificate-oidc-issuer", cosignIssuer,
		filepath.Join(dir, "SHA256SUMS"))
	return err
}

// extractTarball extrae el tar.gz en dst quitando el prefijo (guardian-X.Y.Z/). Solo admite
// directorios, archivos regulares y enlaces simbólicos relativos que queden dentro del árbol.
// Devuelve las rutas relativas de los archivos regulares y enlaces extraídos.
func extractTarball(path, dst, prefix string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, err
	}
	var files []string
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := hdr.Name
		if name == prefix || name+"/" == prefix {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			return nil, fmt.Errorf("entrada fuera de %s: %q", prefix, name)
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" {
			continue
		}
		if err := safeRel(rel); err != nil {
			return nil, err
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			mode := os.FileMode(hdr.Mode).Perm() &^ 0o022
			w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return nil, err
			}
			// Límite por archivo (bomba de descompresión): ningún archivo del repo supera unos MB.
			n, err := io.Copy(w, io.LimitReader(tr, maxEntrySize+1))
			if cerr := w.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return nil, err
			}
			if n > maxEntrySize {
				return nil, fmt.Errorf("entrada demasiado grande en el tarball: %q", rel)
			}
			files = append(files, filepath.ToSlash(rel))
		case tar.TypeSymlink:
			link := hdr.Linkname
			if filepath.IsAbs(link) {
				return nil, fmt.Errorf("enlace absoluto en el tarball: %q → %q", rel, link)
			}
			// El destino debe quedar dentro del árbol extraído.
			resolved := filepath.Clean(filepath.Join(filepath.Dir(rel), link)) // #nosec G305 -- safeRel lo valida a continuación
			if err := safeRel(resolved); err != nil {
				return nil, fmt.Errorf("enlace que sale del árbol: %q → %q", rel, link)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			os.Remove(target)
			if err := os.Symlink(link, target); err != nil {
				return nil, err
			}
			files = append(files, filepath.ToSlash(rel))
		default:
			return nil, fmt.Errorf("tipo de entrada no admitido en el tarball: %q (%c)", rel, hdr.Typeflag)
		}
	}
	sort.Strings(files)
	return files, nil
}

// safeRel rechaza rutas absolutas o que suban con "..".
func safeRel(rel string) error {
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return fmt.Errorf("ruta absoluta en el tarball: %q", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("ruta que sube de directorio en el tarball: %q", rel)
	}
	return nil
}

func isUserData(rel string) bool {
	for _, u := range userData {
		if rel == u || strings.HasPrefix(rel, u+"/") {
			return true
		}
	}
	return false
}

func isProtected(rel string) bool {
	for _, p := range protectedFiles {
		if rel == p {
			return true
		}
	}
	return false
}

// snapshotPrevious copia a .previous/ los archivos que el tarball va a sustituir (los que ya
// existen), más bin/ y VERSION, y anota la versión y la lista de archivos del tarball para
// poder deshacer.
func snapshotPrevious(root, cur string, files []string) error {
	prev := filepath.Join(root, previousDir)
	os.RemoveAll(prev)
	if err := os.MkdirAll(prev, 0o700); err != nil {
		return err
	}
	copyIn := func(rel string) error {
		src := filepath.Join(root, rel)
		st, err := os.Lstat(src)
		if err != nil {
			return nil // no existía: nada que guardar
		}
		if st.IsDir() {
			return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return err
				}
				r, _ := filepath.Rel(root, p)
				return copyFile(p, filepath.Join(prev, r))
			})
		}
		return copyFile(src, filepath.Join(prev, rel))
	}
	for _, rel := range files {
		if isUserData(rel) {
			continue
		}
		if err := copyIn(rel); err != nil {
			return err
		}
	}
	for _, rel := range []string{"bin", "VERSION"} {
		if err := copyIn(rel); err != nil {
			return err
		}
	}
	meta := "version=" + cur + "\n"
	if rev, err := run("git", "-C", root, "rev-parse", "HEAD"); err == nil {
		meta += "git=" + strings.TrimSpace(rev) + "\n"
	}
	if err := os.WriteFile(filepath.Join(prev, "META"), []byte(meta), 0o600); err != nil {
		return err
	}
	// Archivos que el tarball nuevo introduce: al deshacer, se eliminan los que no existían.
	return os.WriteFile(filepath.Join(prev, "NEW_FILES"), []byte(strings.Join(files, "\n")+"\n"), 0o600)
}

// applyTree copia el árbol extraído sobre el repo. Respeta userData y protectedFiles.
// Los archivos quedan con el propietario del directorio raíz (si el repo es de root, de root).
func applyTree(root, tree string, files []string, out io.Writer) ([]string, error) {
	uid, gid := -1, -1
	if st, err := os.Stat(root); err == nil {
		uid, gid = fileOwner(st)
	}
	var written []string
	var kept []string
	for _, rel := range files {
		if isUserData(rel) {
			continue
		}
		src := filepath.Join(tree, rel)
		dst := filepath.Join(root, rel)
		if isProtected(rel) {
			if same, _ := sameContent(src, dst); !same {
				if _, err := os.Stat(dst); err == nil {
					if err := copyFile(src, dst+".new"); err != nil {
						return written, err
					}
					kept = append(kept, rel)
					continue
				}
			} else {
				continue
			}
		}
		if err := copyFile(src, dst); err != nil {
			return written, err
		}
		if uid >= 0 && runtime.GOOS == "linux" {
			os.Lchown(dst, uid, gid)
		}
		written = append(written, rel)
	}
	for _, rel := range kept {
		fmt.Fprintf(out, "AVISO: %s tiene cambios locales; la versión nueva está en %s.new (compárala y fusiónala).\n", rel, rel)
	}
	return written, nil
}

func sameContent(a, b string) (bool, error) {
	ra, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	rb, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return string(ra) == string(rb), nil
}

// copyFile copia un archivo regular o un enlace simbólico conservando el modo.
func copyFile(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		os.Remove(dst)
		return os.Symlink(link, dst)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp-upgrade"
	w, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, in); err != nil {
		w.Close()
		os.Remove(tmp)
		return err
	}
	if err := w.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Chmod(tmp, st.Mode().Perm())
	return os.Rename(tmp, dst)
}

// doRollback restaura .previous/ sobre el repo y elimina los archivos que solo existían en la
// versión nueva. Después levanta la plataforma y pasa el doctor.
func doRollback(o upgradeOpts, h upgradeHooks) error {
	prev := filepath.Join(o.root, previousDir)
	metaRaw, err := os.ReadFile(filepath.Join(prev, "META"))
	if err != nil {
		return fmt.Errorf("no hay versión anterior guardada en %s/", previousDir)
	}
	prevVersion := ""
	for _, line := range strings.Split(string(metaRaw), "\n") {
		if v, ok := strings.CutPrefix(line, "version="); ok {
			prevVersion = v
		}
	}
	cur := installedVersion(o.root)
	fmt.Fprintf(o.out, "deshacer: %s → %s\n", cur, prevVersion)
	if !o.yes && !confirm(o, "¿continuar? [s/N] ") {
		return errors.New("cancelado")
	}
	// 1. Eliminar lo que introdujo la versión nueva y no existía antes.
	if raw, err := os.ReadFile(filepath.Join(prev, "NEW_FILES")); err == nil {
		for _, rel := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if rel == "" || isUserData(rel) {
				continue
			}
			os.Remove(filepath.Join(o.root, rel+".new")) // copia .new de un archivo protegido
			if _, err := os.Lstat(filepath.Join(prev, rel)); err != nil {
				os.Remove(filepath.Join(o.root, rel))
			}
		}
	}
	// 2. Restaurar los archivos guardados.
	n := 0
	err = filepath.Walk(prev, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(prev, p)
		if rel == "META" || rel == "NEW_FILES" {
			return nil
		}
		n++
		return copyFile(p, filepath.Join(o.root, rel))
	})
	if err != nil {
		return fmt.Errorf("restaurar archivos: %v", err)
	}
	fmt.Fprintf(o.out, "archivos restaurados: %d.\n", n)
	if err := h.install(o.root, prevVersion); err != nil {
		return err
	}
	if err := h.stack(o.root); err != nil {
		return fmt.Errorf("levantar la plataforma: %v", err)
	}
	if code := h.doctor(o.root); code != 0 {
		fmt.Fprintf(o.out, "doctor terminó con código %d.\n", code)
	}
	os.RemoveAll(prev)
	fmt.Fprintf(o.out, "Guardian devuelto a %s.\n", prevVersion)
	return nil
}

// ---------------------------------------------------------------- acciones reales (hooks)

// preUpgradeBackup hace una copia con restic si guardian.yaml tiene backup.repository.
func preUpgradeBackup(root string) error {
	c, err := loadConfig(root)
	if err != nil || c.BackupRepo == "" {
		fmt.Println("sin backup.repository en guardian.yaml: no se hace copia previa.")
		return nil
	}
	if err := requireRestic(); err != nil {
		return err
	}
	env, err := resticEnv(root, c)
	if err != nil {
		return err
	}
	fmt.Println("copia previa con restic…")
	if code := backupRun(root, c, env); code != 0 {
		return fmt.Errorf("restic terminó con código %d", code)
	}
	return nil
}

// postSwapInstall deja el binario de la versión nueva en bin/guardianctl, ejecuta las migraciones
// pendientes (internal/migrate) y sincroniza compose/.env (variables nuevas y secretos que
// falten, como hace install.sh).
func postSwapInstall(root, newVersion string) error {
	if err := installBinary(root, newVersion); err != nil {
		return fmt.Errorf("binario: %v", err)
	}
	// Migraciones antes de sincronizar .env: la 0.3.0 reaprovecha LITELLM_MASTER_KEY si existe.
	if err := runMigrations(root, newVersion); err != nil {
		return fmt.Errorf("migraciones: %v", err)
	}
	if err := ensureEnv(root, newVersion); err != nil {
		return fmt.Errorf("compose/.env: %v", err)
	}
	// init --yes con el binario nuevo: sincroniza .env y los define de nftables con guardian.yaml.
	if _, err := os.Stat(filepath.Join(root, "guardian.yaml")); err == nil {
		if out, err := run(filepath.Join(root, "bin", "guardianctl"), "init", "--yes"); err != nil {
			return fmt.Errorf("init --yes: %v\n%s", err, out)
		}
	}
	return nil
}

// installBinary: bin/guardianctl-linux-<arch> del tarball (ya verificado por SHA256SUMS del
// release, que cubre el tarball entero) o compilación local si hay Go ≥ 1.22.
func installBinary(root, newVersion string) error {
	bin := filepath.Join(root, "bin", "guardianctl")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	src := filepath.Join(root, "bin", "guardianctl-linux-"+runtime.GOARCH)
	if _, err := os.Stat(src); err == nil {
		if err := copyFile(src, bin); err != nil {
			return err
		}
		return os.Chmod(bin, 0o755)
	}
	if _, err := exec.LookPath("go"); err == nil {
		cmd := exec.Command("go", "build", "-ldflags", "-s -w -X main.version="+newVersion, "-o", bin, "./cmd/guardianctl")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go build: %v\n%s", err, out)
		}
		return nil
	}
	return fmt.Errorf("no hay bin/guardianctl-linux-%s en el tarball ni Go para compilar", runtime.GOARCH)
}

// ensureEnv replica prepare_env de install.sh: añade variables nuevas de .env.example con su
// valor por defecto, genera los secretos vacíos y fija GUARDIAN_VERSION.
func ensureEnv(root, newVersion string) error {
	envPath := filepath.Join(root, "compose", ".env")
	raw, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	example, err := os.ReadFile(filepath.Join(root, ".env.example"))
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	have := map[string]int{}
	for i, l := range lines {
		if k, _, ok := strings.Cut(l, "="); ok && !strings.HasPrefix(l, "#") {
			have[strings.TrimSpace(k)] = i
		}
	}
	for _, l := range strings.Split(string(example), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok || strings.HasPrefix(l, "#") {
			continue
		}
		if _, found := have[k]; !found {
			lines = append(lines, k+"="+v)
			have[k] = len(lines) - 1
		}
	}
	secrets := map[string]func() string{
		"WEBUI_SECRET_KEY":         func() string { return randomHex(32) },
		"POCKET_ID_ENCRYPTION_KEY": func() string { return randomBase64(32) },
		"GATEWAY_MASTER_KEY":       func() string { return "sk-" + randomHex(24) },
		"GRAFANA_ADMIN_PASSWORD":   func() string { return randomHex(16) },
	}
	for k, gen := range secrets {
		i, ok := have[k]
		if !ok {
			continue
		}
		if _, v, _ := strings.Cut(lines[i], "="); strings.TrimSpace(v) == "" {
			lines[i] = k + "=" + gen()
		}
	}
	if i, ok := have["GUARDIAN_VERSION"]; ok {
		lines[i] = "GUARDIAN_VERSION=" + newVersion
	} else {
		lines = append(lines, "GUARDIAN_VERSION="+newVersion)
	}
	return os.WriteFile(envPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func randomBase64(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var sb strings.Builder
	for i := 0; i+2 < len(b); i += 3 {
		v := uint(b[i])<<16 | uint(b[i+1])<<8 | uint(b[i+2])
		sb.WriteByte(tbl[v>>18&63])
		sb.WriteByte(tbl[v>>12&63])
		sb.WriteByte(tbl[v>>6&63])
		sb.WriteByte(tbl[v&63])
	}
	switch len(b) % 3 {
	case 1:
		v := uint(b[len(b)-1]) << 16
		sb.WriteByte(tbl[v>>18&63])
		sb.WriteByte(tbl[v>>12&63])
		sb.WriteString("==")
	case 2:
		v := uint(b[len(b)-2])<<16 | uint(b[len(b)-1])<<8
		sb.WriteByte(tbl[v>>18&63])
		sb.WriteByte(tbl[v>>12&63])
		sb.WriteByte(tbl[v>>6&63])
		sb.WriteString("=")
	}
	return sb.String()
}

// restartStack: como start_stack en install.sh (pull, up --build --remove-orphans, recarga de Caddy).
func restartStack(root string) error {
	fmt.Println("levantando la plataforma…")
	if _, err := run("docker", composeArgs(root, "pull", "--quiet", "--ignore-buildable")...); err != nil {
		if _, err := run("docker", composeArgs(root, "pull", "--quiet")...); err != nil {
			fmt.Fprintln(os.Stderr, "AVISO: docker compose pull:", err)
		}
	}
	cmd := exec.Command("docker", composeArgs(root, "up", "-d", "--build", "--remove-orphans")...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	run("docker", composeArgs(root, "exec", "-T", "caddy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile")...)
	return nil
}

// runNewDoctor ejecuta el doctor del binario recién instalado (no el que está corriendo).
func runNewDoctor(root string) int {
	cmd := exec.Command(filepath.Join(root, "bin", "guardianctl"), "doctor")
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return 1
	}
	return 0
}

// dryRunReport resume qué haría la actualización: archivos nuevos, modificados y protegidos.
func dryRunReport(o upgradeOpts, tree string, files []string) error {
	var added, changed, protected []string
	for _, rel := range files {
		if isUserData(rel) {
			continue
		}
		dst := filepath.Join(o.root, rel)
		if _, err := os.Lstat(dst); err != nil {
			added = append(added, rel)
			continue
		}
		if same, _ := sameContent(filepath.Join(tree, rel), dst); same {
			continue
		}
		if isProtected(rel) {
			protected = append(protected, rel)
		} else {
			changed = append(changed, rel)
		}
	}
	fmt.Fprintf(o.out, "ensayo: %d archivos nuevos, %d modificados, %d protegidos con cambios locales (quedarían como .new).\n", len(added), len(changed), len(protected))
	for _, rel := range protected {
		fmt.Fprintf(o.out, "  protegido: %s\n", rel)
	}
	for _, rel := range changed {
		fmt.Fprintf(o.out, "  modificado: %s\n", rel)
	}
	for _, rel := range added {
		fmt.Fprintf(o.out, "  nuevo: %s\n", rel)
	}
	fmt.Fprintln(o.out, "no se ha tocado nada. Para actualizar: sudo guardianctl upgrade"+toFlag(o.to))
	return nil
}
