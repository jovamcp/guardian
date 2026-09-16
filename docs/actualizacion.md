# Actualizar Guardian

Desde v0.4 la única ruta de actualización soportada es `guardianctl upgrade`. Sustituye al
`git pull` o al tarball a mano: verifica lo que descarga, hace copia antes de tocar nada y
puede deshacerse.

```bash
sudo ./bin/guardianctl upgrade --check     # ¿hay una versión nueva? (no requiere root)
sudo ./bin/guardianctl upgrade --dry-run   # descarga, verifica y lista qué cambiaría; no toca nada
sudo ./bin/guardianctl upgrade             # actualiza al último release (pide confirmación)
sudo ./bin/guardianctl upgrade --to v0.4.1 # una versión concreta (también para bajar a propósito)
sudo ./bin/guardianctl upgrade --rollback  # vuelve a la versión anterior
```

## Qué hace, en orden

1. Consulta el release en GitHub (`releases/latest` o el tag de `--to`) y descarga a `.upgrade/`
   el tarball, `SHA256SUMS` y su firma. Las prereleases solo se instalan con `--to`.
2. Comprueba el SHA-256 del tarball (en Go) y la firma de `SHA256SUMS` con `cosign` si está
   instalado, contra la identidad del workflow `release.yml` de `jovamcp/guardian` (la misma que
   usa `install.sh`). Sin cosign avisa; con `--require-signature` falla.
3. Extrae el tarball en un directorio temporal y lo valida: solo archivos, directorios y enlaces
   relativos dentro del árbol; `VERSION` debe coincidir con el tag. Nada descargado se ejecuta
   antes de este punto.
4. Hace una copia con restic si `backup.repository` está configurado en `guardian.yaml`. Si la
   copia falla, se detiene (`--no-backup` la omite a propósito).
5. Guarda en `.previous/` los archivos que va a sustituir, `bin/` y `VERSION`.
6. Copia los archivos nuevos sobre el repo **sin tocar** `guardian.yaml`, `compose/.env`,
   `vault/`, `compose/certs/`, `compose/.extra-files`, `.git` ni tus manifiestos en `agents/`.
   Los archivos que sueles editar (`compose/gateway/config.yaml`, `nftables/*.nft`) no se
   sobrescriben: si difieren, la versión nueva queda al lado como `<archivo>.new` y se avisa
   para que la fusiones.
7. Instala el binario nuevo (`bin/guardianctl-linux-<arch>` del tarball, o compila si hay Go),
   añade a `compose/.env` las variables nuevas de `.env.example` generando los secretos que
   falten, ejecuta las migraciones pendientes y `init --yes` (sincroniza `.env` y los `define`
   de nftables con `guardian.yaml`).
8. `docker compose pull` + `up -d --build --remove-orphans`, recarga de Caddy y `doctor` con el
   binario nuevo.

Nunca borra volúmenes: los que queden huérfanos (por ejemplo `guardian_litellm_db_data` al venir
de v0.2) se listan y los borras tú cuando quieras.

## Deshacer

`upgrade --rollback` restaura los archivos guardados en `.previous/`, elimina los que solo
existían en la versión nueva, vuelve a instalar el binario anterior, levanta la plataforma y pasa
el doctor. Solo hay una versión anterior guardada: la de la última actualización.

## Instalaciones con `git clone`

`upgrade` funciona igual: escribe los archivos del release sobre el árbol (que quedará
"modificado" para git). Si prefieres seguir con git, `git fetch --tags && git checkout vX.Y.Z` y
después `sudo ./install.sh` hace lo mismo sin copia previa ni rollback.

## Ensayar contra un servidor propio

`GUARDIAN_RELEASES_API=https://mi-servidor/releases` apunta a una API con el mismo formato que
la de GitHub (`/latest`, `/tags/<tag>`, `assets[].browser_download_url`). Útil para probar un
release candidato antes de publicarlo.
