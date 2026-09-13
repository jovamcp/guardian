# Prompt de trabajo — Fase 4 (semanas 7–8): cierre de v0.1

Derivado de DESIGN.md (hito 4): `guardianctl` completo, plantillas FortiOS/OPNsense, docs y beta.

## Objetivo

Un beta tester clona (o descarga el release), ejecuta `sudo ./install.sh`, contesta cinco preguntas
de `guardianctl init`, pega en su firewall la política que `policy render` le genera y termina con
`doctor` en verde en menos de 15 minutos, con todas las imágenes fijadas por digest.

## Tareas (un commit por tarea, en este orden)

1. **Plan y decisiones.** Este archivo, DESIGN.md §11, CLAUDE.md. Decisiones:
   - `guardian.yaml` es la **única fuente de verdad de la red**: dominio, LAN, zona de IA,
     WireGuard, retención y ntfy. `guardianctl init` lo crea (interactivo o por flags) y de él
     derivan `compose/.env` (DOMAIN, TZ, WG_HOST, LOKI_RETENTION_PERIOD, NTFY_*) y los `define`
     de los dos archivos nftables (`policy render nftables`). Los secretos siguen solo en `.env`.
   - Las plantillas del firewall perimetral se renderizan con `text/template` desde
     `guardian.yaml`: FortiOS (CLI) y OPNsense (guía paso a paso con los valores ya puestos;
     no se genera XML de importación en v0.1).
   - **Digest antes del release**: `scripts/pin-images.sh` resuelve cada `image:` al digest del
     índice multi-arch y reescribe el compose (`repo:tag@sha256:…`). En desarrollo se permite
     tag; `main` lleva digest desde esta fase.
   - Release = tarball + SHA-256 generado por `make release`; la firma con cosign queda para v0.2.
   - CI en GitHub Actions: `go vet/test`, `bash -n`, `docker compose config`, `vector validate`,
     `nft -c` y `caddy validate`.
2. **`guardian.yaml` e `init`.** Esquema en `guardian.yaml.example`; `guardianctl init`
   (flags `--domain --lan --ai-vlan --ai-cidr --ai-host --wg-host --wg-cidr --tz
   --retention-days --ntfy-url --ntfy-topic`, o preguntas si hay TTY), escribe `guardian.yaml`,
   sincroniza `compose/.env`, exporta la CA y llama a `policy render nftables`. Idempotente.
3. **`policy render`.** `nftables` (reescribe los `define`), `fortios` (plantilla existente con
   valores reales, a stdout o `--out`), `opnsense` (guía Markdown con valores). Validar con
   `nft -c` en la VM y revisar la salida FortiOS a mano.
4. **Digests y release.** `scripts/pin-images.sh`, compose fijado, `make release` con
   `VERSION`, `guardianctl version` leyendo la versión en tiempo de compilación, CI.
5. **Docs y beta.** `docs/beta.md` (requisitos, cronómetro de 15 min, checklist, qué reportar,
   plantilla de issue), `CHANGELOG.md`, README y `docs/instalacion.md` con el flujo de `init`,
   `.github/ISSUE_TEMPLATE`.
6. **Ensayo general.** Instalación completa en VM limpia con el flujo final (release tarball,
   `install.sh`, `init` con flags, `doctor`) y medición del tiempo.

## Criterios de aceptación

- `guardianctl init --domain ai.home --lan 10.10.10.0/24 --ai-cidr 10.20.0.0/24 --ai-host 10.20.0.10 …`
  deja `guardian.yaml`, `.env` y los `define` de nftables coherentes; `nft -c` pasa.
- `policy render fortios` produce CLI válida con las redes del `guardian.yaml`; `opnsense`, una
  guía sin marcadores sin sustituir.
- Todas las imágenes del compose llevan `@sha256:`; `docker compose pull` funciona en la VM.
- CI en verde en GitHub; `make release` genera `dist/guardian-<versión>.tar.gz` y su `.sha256`.
- Instalación limpia en VM desde el tarball en menos de 15 minutos con `doctor` 13/13.

## Restricciones

Las de siempre. No se añaden servicios ni puertos. Los 5 beta testers reales quedan fuera de lo
que puede hacer esta sesión: se deja el programa listo (documentación, plantillas, release).
