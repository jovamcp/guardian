# Changelog

Formato basado en [Keep a Changelog](https://keepachangelog.com/es/1.1.0/). Versionado semántico.

## [0.4.0] — sin publicar

### Añadido
- `guardianctl upgrade [--check | --dry-run] [--to vX.Y.Z] [--rollback]`: actualización a un release
  firmado (SHA-256 en Go, cosign si está), copia previa con restic, árbol anterior en `.previous/`,
  archivos del usuario y protegidos (`compose/gateway/config.yaml`, `nftables/*.nft` → `.new`)
  intactos, `.env` sincronizado con `.env.example`, `compose up` y `doctor`. `docs/actualizacion.md`.
- `internal/migrate` y `guardianctl migrate [--from X] [--dry-run | --mark]`: migraciones idempotentes
  por versión, registro en `compose/.migrated`, comprobación en `doctor`. Migración 0.3.0:
  `LITELLM_*` → `GATEWAY_MASTER_KEY`, `compose/litellm/` fuera, volúmenes huérfanos listados.

### Cambiado
- `make restart` reiniciaba el servicio `litellm` (inexistente desde v0.3); ahora `gateway`.
- Sin restos de LiteLLM en código, compose, Vector ni CI (los paneles siguen leyendo logs antiguos).

## [0.3.0] — 2026-09-16

### Añadido
- **gd-gateway**: gateway propio en Go (stdlib) compatible con OpenAI que sustituye a LiteLLM y
  Postgres. Llaves virtuales (`guardianctl key`), modelos por llave, límite rpm, streaming,
  auditoría de metadatos, API de administración, ~5 MB de RAM.
- **Multi-nodo**: varios nodos Ollama por modelo con salud, reparto ponderado y failover;
  `doctor` avisa de nodos caídos.
- **Cloud burst controlado**: upstreams compatibles con OpenAI marcados `cloud`, permiso por
  llave (`--cloud`), presupuesto mensual por llave y global con tabla de precios, `402` al
  agotarlo y alerta ntfy al 80 %.
- `docs/gateway.md` (uso, nodos, cloud, migración desde v0.2).

### Cambiado
- `api.<DOMAIN>` y `http://gateway:4000/v1` apuntan al gateway; los agentes usan `gateway`
  (antes `litellm`). `GATEWAY_MASTER_KEY` reemplaza a `LITELLM_*` en `.env`.
- Los directorios `compose/vector` y `compose/gateway` se montan enteros (inodos al reemplazar).
- Las copias incluyen `gateway_data`; el `pg_dump` solo se hace si aún existe LiteLLM.

### Seguridad (revisión completa, `docs/seguridad.md`)
- Los timers de systemd exigen que el repo, el binario y el runner sean de root (escalada local).
- `mounts:` de los manifiestos confinados a `agents/`; `docker.sock` prohibido; digest de 64 hex.
- Gateway: `X-Forwarded-For` solo desde Caddy; límite de 20 fallos de autenticación por minuto e IP.
- `no-new-privileges` en todos los servicios y `cap_drop ALL` donde es seguro.
- gosec, staticcheck y gitleaks en CI; `.gitleaks.toml` con las excepciones de prueba.

### Eliminado
- Servicios `litellm` y `litellm-db`; `compose/litellm/`. Las llaves de LiteLLM no se migran.

## [0.2.0] — 2026-09-12

### Añadido
- **Copias de seguridad** con restic: `guardianctl backup init|run|list|restore|schedule`,
  contraseña en el vault, pg_dump de LiteLLM, volúmenes como tar, timer de systemd y aviso en
  `doctor` si la última copia es antigua.
- **Releases firmados** con cosign (keyless) desde GitHub Actions; `install.sh` verifica la firma
  de `SHA256SUMS` si cosign está instalado.
- **gVisor opcional** para agentes: `install.sh --with-gvisor` y `sandbox.runtime: gvisor`.
- **Dominio público con DNS-01**: `tls.mode: acme-dns` (Cloudflare o DuckDNS) con imagen de
  Caddy propia construida en local; sin puertos nuevos.
- **Panel "Guardian · Estado"** en Grafana: `doctor --report` cada 15 minutos vía Vector.
- **UniFi**: `policy render unifi`. **Proxmox LXC**: `docs/proxmox-lxc.md` y comprobación en `doctor`.

### Cambiado
- El gateway LiteLLM se resuelve por `/etc/hosts` dentro de los agentes (necesario con gVisor).
- `make release` genera `SHA256SUMS` de forma portable; el workflow de release publica firmas.

### Decidido
- `userns-remap` descartado: gVisor cubre el aislamiento adicional sin recrear `/var/lib/docker`.

## [0.1.0-beta.1] — 2026-09-12

Primera beta pública. Todo probado en VMs de Debian 12 y Ubuntu 24.04 (arm64); pendiente de
hardware x86-64 real y de móvil fuera de casa.

### Base (Fase 1)
- Caddy con TLS interno y cabeceras seguras; CA exportada a `compose/certs/root.crt`.
- Pocket ID (passkeys) como proveedor OIDC; Open WebUI solo con login OIDC.
- Ollama aislado en la red `gd_ai`, sin puertos publicados.
- wg-easy 15 para acceso remoto; únicos puertos publicados 443/tcp y 51820/udp.
- `install.sh` idempotente (Docker desde el repositorio oficial) y nftables con regla de
  rescate SSH, restricciones de origen en `DOCKER-USER` y persistencia segura con Docker.

### Agentes (Fase 2)
- LiteLLM con llaves virtuales en `api.<DOMAIN>` (`guardianctl key`).
- Red interna `gd_agents`; Squid y Blocky con allowlist por agente (`policy render egress`).
- Sandbox: seccomp y AppArmor propios, uid 10000, solo lectura, sin capacidades
  (`guardianctl agent run`); vault con `age` (`guardianctl secret`).

### Auditoría (Fase 3)
- Vector → Loki (30 días) → Grafana en `logs.<DOMAIN>` con OIDC; socket de Docker solo a través
  de un proxy de solo lectura.
- Paneles Accesos, LLM y Egreso; alertas a ntfy (egreso denegado, fallos de login, descartes).
- `schedule.cron` de los agentes como timers de systemd (`guardianctl agent schedule`).

### Cierre (Fase 4)
- `guardian.yaml` + `guardianctl init`; `policy render nftables|fortios|opnsense`.
- Imágenes fijadas por digest, `make release` (tarball + SHA256SUMS), CI en GitHub Actions.
- Programa beta (`docs/beta.md`).

### Ensayo general
- VM limpia de Ubuntu 24.04 (arm64): desde el tarball verificado, `install.sh` sin terminal tarda
  8 min (incluye instalar Docker y descargar 13 imágenes) y `doctor` queda 13/13 (con aviso de
  ntfy sin configurar) medio minuto después. Objetivo de la beta: < 15 min con los pasos manuales.

### Conocido
- Pocket ID 2.14.0 puede reiniciarse solo en VMs con saltos de reloj ("host is not registered").
- Sin `userns-remap` (v0.2), sin firma cosign (v0.2), plantilla UniFi pendiente.
