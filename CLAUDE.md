# CLAUDE.md — guía para trabajar en este repo

## Qué es
Guardian: capa de seguridad y red para IA local y agentes en homelab. Se instala delante del runtime
(Ollama, llama.cpp, vLLM) y añade identidad (OIDC), acceso remoto por WireGuard, zonificación con
nftables, sandbox de agentes con egreso por allowlist y auditoría. Diseño completo en `DESIGN.md`.
Documentación y comentarios en español; identificadores, archivos y commits en inglés.

## Fase actual
**Fase 1 (semanas 1–2): base funcional.** Caddy + Pocket ID + Open WebUI + Ollama + wg-easy,
`install.sh`, nftables, `guardianctl status/doctor`. Nada de agentes, LiteLLM, egreso ni auditoría todavía.
Prompt de trabajo: `docs/prompts/01-fase-1.md`.

## Reglas duras (no negociables)
1. Ollama nunca publica puertos en el host; solo existe en la red Docker `gd_ai`.
2. Los únicos puertos publicados son 443/tcp (Caddy) y 51820/udp (WireGuard).
3. Nada de `curl | bash` en instaladores ni docs.
4. Antes de un release, imágenes fijadas por digest; en desarrollo se permite tag.
5. Ninguna regla de nftables puede bloquear SSH desde la LAN: regla de rescate siempre presente.
6. Secretos solo en `compose/.env` (ignorado por git) o en `vault/`. Nunca en compose, código ni docs.
7. Todo cambio de nftables se valida con `nft -c -f` antes de aplicarse.

## Decisiones técnicas
- Docker rootful para la plataforma (WireGuard necesita NET_ADMIN y módulo del kernel; GPU no va bien en rootless).
  Los agentes (Fase 2) correrán con userns-remap, cap-drop ALL, seccomp y AppArmor; gVisor/podman en v0.2.
- Docker publica por FORWARD, no por INPUT: las restricciones de origen para 443/51820 van en la cadena
  DOCKER-USER y en el firewall de la VLAN; la cadena input de nftables cubre SSH y el log de descartes.
- TLS interno de Caddy (`tls internal`) en v0.1; la CA raíz se exporta a `compose/certs/root.crt`.
  Dominio real con DNS challenge en v0.2.
- `guardianctl` en Go, solo biblioteca estándar.
- Redes de ejemplo: LAN 10.10.10.0/24; zona de IA VLAN 20, 10.20.0.0/24, host 10.20.0.10; WireGuard 10.8.0.0/24.
- Versiones verificadas (2026-09): Pocket ID v2 (exige `ENCRYPTION_KEY`), Open WebUI `main`
  (redirect URI `/oauth/oidc/callback`), wg-easy 15 (sin `WG_HOST`; asistente web o `INIT_*`).

## Convenciones
- Un PR (o commit) por tarea; el mensaje explica qué cambió y con qué fuente se verificó.
- Cada servicio del compose lleva `restart: unless-stopped`, `healthcheck` y rotación de logs (`x-logging`).
- Bash siempre con `set -euo pipefail`; validar con `bash -n` y, si está, `shellcheck`.
- Go: `gofmt`, `go vet ./...`, sin dependencias externas.
- Todo cambio en compose indica qué variable cambió y la URL de la documentación consultada.

## Cómo probar
```bash
make up            # levanta la plataforma
make doctor        # comprobaciones (root)
make logs          # logs en vivo
make nft-check     # valida nftables sin aplicar
# Desde otro equipo de la LAN:
nmap -sT -p- <ip-host>            # solo 443 (y 22 si hay SSH)
nmap -sU -p 51820 <ip-host>       # open|filtered
curl -m 3 http://<ip-host>:11434  # debe fallar
# Desde el contenedor de Open WebUI (única ruta legítima a Ollama):
docker compose --env-file compose/.env -f compose/docker-compose.yml exec open-webui \
  curl -s http://ollama:11434/api/tags
```
