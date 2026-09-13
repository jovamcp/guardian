# Guardian — Diseño técnico v0.1

> Nombre provisional. Documento vivo: cambia con cada hito. Decisiones abiertas al final.

## 1. Propósito y alcance

Guardian es una capa de seguridad y red que se instala **delante** de cualquier runtime de IA
local (Ollama, llama.cpp, vLLM; también sobre Umbrel o ZimaOS) y añade lo que ninguno trae de serie:

- **Identidad**: login único con OIDC y passkeys.
- **Acceso remoto** solo por WireGuard; nada de port-forwarding a la aplicación.
- **Zonificación** de red con nftables en el host y plantillas para el firewall perimetral.
- **Sandbox de agentes** con egreso controlado por allowlist por agente.
- **Auditoría**: todo flujo deja rastro consultable.

Guardian no sustituye el runtime ni el chat: los protege.

**Cliente objetivo v0.1**: homelabber con un mini-PC Linux que ya corre Ollama y Open WebUI y
quiere ejecutar agentes (OpenClaw, Hermes Agent, n8n) sin exponer su red ni sus credenciales.

**Fuera de v0.1**: multi-tenant, cloud burst, hardware propio, dashboard propio (se usa Grafana).

## 2. Principios

1. **Denegar por defecto.** Toda red, puerto y destino está cerrado salvo que se abra a propósito.
2. **Nada expuesto sin identidad.** Cada servicio web pasa por Caddy y por Pocket ID.
3. **Los secretos nunca viven en el contenedor del agente.** Se inyectan cifrados y de un solo uso, o los usa un proxy en nombre del agente.
4. **Todo flujo deja rastro.** Acceso, llamadas al LLM, egreso y cambios de configuración.
5. **Funciona sobre lo que el usuario ya tiene.** Instalación en menos de 15 minutos sobre Debian/Ubuntu con Docker.
6. **Reproducible.** Imágenes por digest en releases, configuración declarativa (`guardian.yaml`), sin `curl | bash`.

## 3. Modelo de amenazas

| Amenaza | Vector | Control en Guardian |
|---|---|---|
| API de Ollama expuesta | `11434/tcp` publicado en el host o en la LAN; cualquier dispositivo consume GPU, borra modelos o extrae prompts | Ollama solo en la red Docker `gd_ai`, sin `ports:`; `doctor` comprueba que nada escucha en 11434; nftables y firewall de VLAN |
| Agente comprometido por prompt injection | Contenido malicioso hace que el agente ejecute acciones o filtre credenciales | Sandbox (no root, read-only, cap-drop ALL, seccomp, AppArmor), red `gd_agents` sin ruta por defecto, egreso por proxy con allowlist, llaves LLM virtuales con cuota |
| Movimiento lateral a NAS / Home Assistant | Agente o runtime pivota a la LAN | Zona de IA en VLAN propia; política AI→LAN deny con log en el firewall; `gd_agents` es `internal` |
| Exfiltración por HTTPS/DNS | Agente envía datos a un dominio arbitrario o codifica en consultas DNS | Squid con allowlist por agente (solo CONNECT a dominios permitidos), Blocky como único resolvedor, DNS directo bloqueado, todo registrado |
| Supply chain de imágenes y modelos | Imagen o modelo manipulado | Imágenes por digest en releases; manifiestos con digest obligatorio; cosign y verificación de modelos en v0.2 |
| Acceso remoto por port-forwarding | Puertos de aplicación abiertos a Internet | Solo 51820/udp (WireGuard) expuesto; 443 restringido a LAN/AI/WG en DOCKER-USER y en el firewall |
| Escalada en el host | Contenedor escapa o usa el socket de Docker | Agentes con userns-remap y sin socket Docker; plataforma con capacidades mínimas; regla de rescate SSH para no perder el host |

## 4. Arquitectura

### Zonas de red

| Zona | Ejemplo | Contenido |
|---|---|---|
| LAN de usuarios | `10.10.10.0/24` | Portátiles, móviles, NAS, Home Assistant |
| Zona de IA (VLAN 20) | `10.20.0.0/24`, host `10.20.0.10` | Host Guardian y GPU |
| WireGuard | `10.8.0.0/24` | Clientes remotos |

### Redes Docker

| Red | Tipo | Servicios |
|---|---|---|
| `gd_front` | bridge | Caddy, Pocket ID, wg-easy, Grafana |
| `gd_ai` | bridge, sin puertos publicados | Ollama, Open WebUI, gateway LiteLLM |
| `gd_agents` | `internal`, sin ruta por defecto | Contenedores de agentes |
| `gd_egress` | bridge | Squid (proxy) y Blocky (DNS) |

### Componentes

| Componente | Función | Fase |
|---|---|---|
| Caddy | TLS (interno en v0.1), cabeceras de seguridad, reverse proxy | 1 |
| Pocket ID | Proveedor OIDC con passkeys | 1 |
| Open WebUI | Chat, cliente OIDC | 1 |
| Ollama | Runtime de modelos | 1 |
| wg-easy (o Tailscale) | Acceso remoto WireGuard con panel y QR | 1 |
| nftables | Firewall del host + DOCKER-USER | 1 |
| guardianctl | `init`, `agent`, `key`, `secret`, `policy render`, `status`, `doctor` | 1–4 |
| LiteLLM proxy | API compatible OpenAI, llaves virtuales por agente, cuotas | 2 |
| Squid + Blocky | Egreso HTTPS con allowlist por agente, DNS filtrado | 2 |
| Docker + seccomp/AppArmor | Sandbox de agentes | 2 |
| Vector → Loki → Grafana | Auditoría centralizada | 3 |
| Grafana alerting → ntfy | Alertas al móvil | 3 |

### Flujos

1. **Usuario en la LAN**: navegador → `https://<DOMAIN>` (Caddy) → Open WebUI → redirección a Pocket ID → passkey → sesión → Open WebUI habla con Ollama por `gd_ai`.
2. **Aplicación con llave**: app → `https://api.<DOMAIN>` (Caddy) → LiteLLM valida la llave virtual, aplica cuota y modelo permitido → Ollama.
3. **Agente por proxy**: contenedor en `gd_agents` → `HTTPS_PROXY` = Squid → allowlist del agente → Internet; DNS solo vía Blocky; LLM solo vía LiteLLM con su llave.
4. **Admin por WireGuard**: móvil → `51820/udp` → wg-easy → `10.8.0.0/24` → Caddy `443` → mismos servicios que en LAN. Nada más está expuesto.

## 5. Sandbox de agentes

### Manifiesto (`agents/<nombre>.yaml`)

```yaml
name: openclaw
image: ghcr.io/openclaw/openclaw@sha256:…      # digest obligatorio
llm:
  models: [llama3.1:8b]
  key: auto                                    # llave virtual creada por guardianctl
egress:
  allow: [api.github.com, pypi.org]
services:
  github:
    url: https://api.github.com
    token: vault:openclaw/github_token         # secreto cifrado en vault/
resources: {cpus: "2", memory: 2g, pids: 256}
schedule: {cron: ""}
```

### Perfil de ejecución

- Usuario no root (`--user`), `userns-remap` en el daemon.
- Sistema de archivos `read-only` con `tmpfs` para `/tmp` y el directorio de trabajo.
- `cap-drop ALL`, `no-new-privileges`, seccomp `sandbox/seccomp-agent.json`, AppArmor `guardian-agent`.
- Red `gd_agents` (internal); DNS y `HTTPS_PROXY` apuntan a Guardian; sin acceso al socket de Docker.
- Secretos cifrados con age/sops en `vault/`, descifrados en el momento de arrancar e inyectados como archivos `0400` en un `tmpfs`; nunca como variables de entorno persistentes.
- Límites de CPU, memoria y PIDs desde el manifiesto.

## 6. `guardian.yaml`

Configuración declarativa que lee `guardianctl` (ver `guardian.yaml.example`):

```yaml
domain: ai.home
lan_cidr: 10.10.10.0/24
ai_zone: {vlan: 20, cidr: 10.20.0.0/24, host_ip: 10.20.0.10}
remote: {provider: wireguard, cidr: 10.8.0.0/24, endpoint: ""}
runtime: {kind: ollama, gpu: none}
firewall: {vendor: fortios, parent_interface: internal, lan_interface: internal, wan_interface: wan1}
audit: {retention_days: 30}
alerts: {ntfy: {url: "", topic: guardian}}
```

## 7. Instalación

1. Obtener el código: release firmado (tarball + firma) o `git clone`.
2. `sudo ./install.sh [--with-nftables]`: instala Docker desde el repo oficial, crea `compose/.env`, exporta la CA de Caddy y levanta la plataforma.
3. `guardianctl init`: exporta la CA e imprime los siguientes pasos (Fase 1); genera `guardian.yaml` (Fase 4).
4. `guardianctl policy render`: genera la política del firewall perimetral desde `policies/`.
5. `guardianctl doctor`: verifica puertos, servicios, CA y aislamiento de Ollama.

## 8. Estructura del repo

```
README.md CLAUDE.md DESIGN.md LICENSE .env.example .gitignore guardian.yaml.example Makefile install.sh go.mod
cmd/guardianctl/        CLI en Go (solo stdlib)
compose/                docker-compose.yml, caddy/Caddyfile, certs/ (CA exportada, ignorada)
nftables/               ruleset del host
policies/{fortios,opnsense,unifi}/  plantillas del firewall perimetral
sandbox/                seccomp, AppArmor y runner (Fase 2)
agents/examples/        manifiestos de ejemplo
docs/                   instalación y prompts de trabajo por fase
```

## 9. Plan

### v0.1 — cuatro hitos

| Hito | Semanas | Contenido | Criterio de aceptación |
|---|---|---|---|
| 1 Base | 1–2 | Caddy, Pocket ID, Open WebUI, Ollama, wg-easy, nftables, `install.sh`, `doctor` básico | `nmap` desde la LAN muestra solo 443 (y 22); Ollama inalcanzable desde la LAN; login OIDC sin contraseña; chat por WireGuard desde fuera; `make doctor` en verde |
| 2 Agentes | 3–4 | LiteLLM con llaves virtuales, red `gd_agents`, Squid + Blocky, primer sandbox | Un agente de ejemplo llama al LLM con su llave, solo alcanza dominios de su allowlist, no ve la LAN ni el socket Docker |
| 3 Auditoría | 5–6 | Vector → Loki → Grafana, alertas a ntfy | Cada login, llamada LLM y descarte de egreso aparece en Grafana; alerta en el móvil al bloquear un destino |
| 4 Cierre | 7–8 | `guardianctl` completo, plantillas FortiOS/OPNsense, docs, 5 beta testers | 5 instalaciones externas completadas en menos de 15 min con el doctor en verde |

### v0.2
Dashboard propio, gVisor para agentes, verificación con cosign, plantilla UniFi, copias con restic, despliegue en LXC de Proxmox, dominio real con DNS challenge.

### v0.3
Gateway propio en Go (sustituye LiteLLM), multi-nodo, cloud burst controlado.

## 10. Licencia y marca

- Núcleo: **AGPL-3.0** (archivo `LICENSE`). Contribuciones bajo DCO (`Signed-off-by`).
- Tier Pro: propietario, en otro repositorio (modelo open-core).
- Registrar la marca antes del primer release público; "Guardian" es un nombre provisional.

## 11. Decisiones abiertas

### Tomadas durante la Fase 2

- **`userns-remap` se pospone a v0.2.** Es global al daemon, recrea `/var/lib/docker` bajo otro
  uid (desaparecen imágenes y volúmenes ya creados) y wg-easy necesita el userns del host. Los
  agentes corren con uid/gid fijos no privilegiados (`10000:10000`), `cap-drop ALL`,
  `no-new-privileges`, seccomp y AppArmor propios, sistema de archivos de solo lectura.
- **La identidad de red de un agente es su IP fija en `gd_agents`.** Squid (allowlist de
  `CONNECT` por `dstdomain`) y Blocky (grupo de cliente con DNS denegado por defecto) filtran
  por IP de origen. `guardianctl` asigna la IP a partir del manifiesto y renderiza ambas
  configuraciones (`policy render egress`).
- **LiteLLM con Postgres** (`gd-litellm-db`, solo en `gd_ai`): las llaves virtuales lo exigen.

### Tomadas durante la Fase 3

- **Vector lee los logs a través de un `docker-socket-proxy` de solo lectura** en la red interna
  `gd_audit`, nunca con el socket montado: un compromiso del recolector no da control del daemon.
- **Descartes de nftables por `journald`** (montaje de solo lectura), sin rsyslog.
- **Loki monolítico en disco** con retención por compactor (`audit.retention_days`).
- **Grafana en `logs.<DOMAIN>` con OIDC de Pocket ID** y todo provisionado desde el repo.
- **Alertas por webhook directo a ntfy** (`?template=grafana`), sin servicio intermedio.
- **`schedule.cron` → timers de systemd** generados por `guardianctl agent schedule`.

### Tomadas durante la Fase 4

- **`guardian.yaml` es la fuente de verdad de la red**; `guardianctl init` lo crea y de él derivan
  `compose/.env` (valores no secretos) y los `define` de nftables (`policy render nftables`).
- **Plantillas perimetrales con `text/template`**: FortiOS genera CLI; OPNsense genera una guía con
  los valores puestos (sin XML de importación en v0.1).
- **Digest antes del release** con `scripts/pin-images.sh`; `main` va fijado desde la Fase 4.
- **Release = tarball + SHA-256** (`make release`); firma con cosign en v0.2.

### Tomadas para v0.2

- **Dashboard propio = panel de estado en Grafana** alimentado por `doctor --json` (sin nueva
  superficie de autenticación).
- **Copias con restic** desde `guardianctl backup`, contraseña en el vault, timer de systemd.
- **Releases firmados con cosign keyless** en GitHub Actions; `install.sh` verifica si cosign está.
- **gVisor opcional para agentes** (`sandbox.runtime: gvisor`); `userns-remap` descartado.
- **Dominio real con DNS challenge** mediante imagen de Caddy propia (xcaddy + módulo DNS).
- **UniFi y Proxmox LXC** como guías renderizadas/documentadas, con comprobaciones en `doctor`.

### Abiertas

- wg-easy vs Tailscale como opción por defecto de acceso remoto.
- LiteLLM (Python, pesado) vs gateway propio desde v0.2.
- Pocket ID vs Authelia si hace falta LDAP o políticas por grupo más finas.
- Squid con allowlist de CONNECT vs proxy TLS-terminating (rompe pinning, más visibilidad).
- ¿Podman rootless para la plataforma cuando la GPU lo soporte bien?
- Formato del vault: sops+age en archivos vs un pequeño servicio de secretos.
