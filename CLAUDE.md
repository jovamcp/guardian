# CLAUDE.md — guía para trabajar en este repo

## Qué es
Guardian: capa de seguridad y red para IA local y agentes en homelab. Se instala delante del runtime
(Ollama, llama.cpp, vLLM) y añade identidad (OIDC), acceso remoto por WireGuard, zonificación con
nftables, sandbox de agentes con egreso por allowlist y auditoría. Diseño completo en `DESIGN.md`.
Documentación y comentarios en español; identificadores, archivos y commits en inglés.

## Fase actual
**Fase 2 (semanas 3–4): agentes — en curso.** LiteLLM con llaves virtuales, redes `gd_agents` y
`gd_egress`, Squid + Blocky con allowlist por agente, sandbox (seccomp, AppArmor, runner) y vault
con age. Plan y criterios: `docs/prompts/02-fase-2.md`. La Fase 1 (base) está completa y probada
en VMs (`docs/prompts/01-fase-1.md`). Nada de auditoría (Fase 3) todavía.

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
- Versiones verificadas (2026-09): Pocket ID v2.14 (exige `ENCRYPTION_KEY`; los clientes OIDC nacen
  restringidos por grupo), Open WebUI `main`/0.11 (redirect URI `/oauth/oidc/callback`;
  `ENABLE_LOGIN_FORM` y `ENABLE_SIGNUP` son PersistentConfig: solo se leen en el primer arranque),
  wg-easy 15.4 (sin `WG_HOST`; asistente web o `INIT_*`).
- nftables: `nftables.service` de Debian/Ubuntu hace `flush ruleset` en `ExecStop` y el
  `/etc/nftables.conf` por defecto también; ambos borran las tablas de Docker. `install.sh` los
  neutraliza (drop-in + comentario). Nunca uses `flush ruleset`; reaplica con `make nft-apply`.
- Los clientes WireGuard llegan al host enmascarados con la IP de wg-easy (172.28.10.0/24) y
  por INPUT (docker-proxy, hairpin): esa subred debe estar permitida en 443 en ambos archivos.
- Pocket ID 2.14.0 reinició periódicamente en las VMs ("host is not registered"), con y sin TZ y a la
  misma hora en ambas VMs del mismo anfitrión: probable salto de reloj de la VM. Vigilar en hardware real.
- Pruebas en Apple Silicon (Lima/vz): Open WebUI muere con SIGILL en `cryptography`; usa
  `OPENSSL_armcap=0` en un override fuera del repo. No afecta a x86-64.

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
