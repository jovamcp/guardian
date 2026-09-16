# Revisión de seguridad de Guardian

Revisión completa realizada el 2026-09-12 sobre v0.3 (`main`), con análisis estático (gosec,
staticcheck, shellcheck, hadolint, trivy config, gitleaks sobre todo el historial) y revisión
manual del código de `guardianctl`, `gd-gateway`, `install.sh`, el compose, nftables, Squid,
Blocky, Vector y las plantillas. Este documento recoge qué se encontró, qué se corrigió y qué
riesgos quedan aceptados y por qué. Se actualiza en cada release. **Actualizada el 2026-09-16
para v0.4** (`upgrade`, migraciones, modelos, agentes reales): sección "Novedades de v0.4" y filas
nuevas en riesgos aceptados.

## Modelo de confianza (resumen)

- **Límite de confianza = el host y su daemon de Docker.** Quien controle root o el socket de
  Docker controla Guardian. Todo lo demás se diseña para que un componente comprometido no
  llegue ahí: agentes sin socket ni capacidades, Vector detrás de un proxy de solo lectura,
  redes internas, gateway sin shell.
- **Secretos** solo en `compose/.env` (0600) y `vault/` (age). Nunca en imágenes, código, logs
  ni variables de entorno de agentes (archivos 0400).
- **Superficie expuesta**: 443/tcp (Caddy, solo LAN/VPN por nftables) y 51820/udp (WireGuard).
  Todo lo web pasa por Pocket ID. La API del gateway exige llave virtual; `/admin` exige la
  master key.

## Hallazgos corregidos en esta revisión

| # | Hallazgo | Riesgo | Corrección |
|---|---|---|---|
| 1 | Los timers de systemd ejecutaban como root `sandbox/runner.sh` y `bin/guardianctl` desde un repo propiedad del usuario que hizo `git clone`. Un usuario sin privilegios que pudiera escribir ahí obtendría root en la siguiente ejecución. | **Alto** (escalada local) | `agent schedule apply`, `backup schedule apply` y `doctor schedule apply` se niegan si el repo, el binario o el runner no son de root o son escribibles por otros; `doctor` lo comprueba mientras existan timers. Documentado: instalar en `/opt/guardian` como root si se usan timers. |
| 2 | `mounts:` de un manifiesto aceptaba rutas absolutas o `..`: un manifiesto podía montar `vault/`, `compose/.env` o `/var/run/docker.sock` (solo lectura, pero suficiente para exfiltrar). | **Alto** (si el manifiesto no es de confianza) | Solo rutas relativas que resuelvan (tras enlaces simbólicos) dentro de `agents/`; destinos absolutos fuera de `/run/guardian`, `/proc`, `/sys`, `/dev`; `docker.sock` prohibido. Verificado en la VM con tres manifiestos maliciosos. |
| 3 | `image:` solo exigía que contuviera `@sha256:`; un digest truncado o falso pasaba la validación. | Medio | Expresión regular `@sha256:[0-9a-f]{64}$`. |
| 4 | El gateway confiaba en `X-Forwarded-For` de cualquier cliente: un agente podía falsear su IP en la auditoría. | Medio (integridad de auditoría) | Solo se acepta la cabecera cuando la conexión viene de Caddy (172.28.10.0/24) o de loopback. |
| 5 | Sin límite de intentos de autenticación fallidos en el gateway (llaves o master key) desde la LAN, la VPN o un agente. | Medio (fuerza bruta) | 20 fallos por minuto y por IP → `429` (verificado: 20 × 401 y después 429). El limitador poda entradas antiguas. |
| 6 | La mayoría de servicios corrían con las capacidades por defecto de Docker y sin `no-new-privileges`. | Medio (defensa en profundidad) | `no-new-privileges` en los 12 servicios; `cap_drop: ALL` en caddy y blocky (+`NET_BIND_SERVICE`), loki, grafana, docker-socket-proxy (+`NET_BIND_SERVICE,SETGID,SETUID`) y gateway. wg-easy conserva `NET_ADMIN`/`SYS_MODULE` (necesarios); squid, vector, pocket-id, open-webui y ollama conservan las suyas por defecto (cambian de usuario o escriben en volúmenes). |
| 7 | `agent schedule remove <nombre>` no validaba el nombre antes de construir rutas de unidades. | Bajo | Se valida contra el mismo patrón que el manifiesto. |
| 8 | Un token ficticio en CI disparaba gitleaks y ocultaría fugas reales al acostumbrarse al aviso. | Bajo | `.gitleaks.toml` con las dos excepciones de prueba; gitleaks, gosec y staticcheck forman parte de CI. |

## Revisado y considerado correcto

- **Llaves del gateway**: se almacenan como SHA-256; el valor solo se devuelve al crearlas;
  comparación de la master key en tiempo constante; identificadores aleatorios (`crypto/rand`).
- **Proxy del gateway**: solo reenvía a los upstreams declarados por el administrador (no hay
  SSRF controlable por el cliente); cuerpo limitado a 32 MB; solo se reenvían `Content-Type` y
  `Cache-Control` de la respuesta; no se registran prompts, respuestas ni cabeceras de
  autorización; `read_only`, `cap_drop ALL`, usuario no root, imagen distroless.
- **Agentes**: uid 10000, raíz de solo lectura, tmpfs `noexec`, `cap-drop ALL`,
  `no-new-privileges`, seccomp y AppArmor propios, red interna sin ruta, secretos como archivos
  0400 en tmpfs que se destruyen al terminar, llave LLM efímera revocada al salir.
- **Egreso**: Squid solo `CONNECT` a 443 hacia la allowlist por IP de agente; nada de HTTP en
  claro, IPs literales ni redes privadas; Blocky deniega todo salvo la allowlist; el gateway se
  resuelve por `/etc/hosts`.
- **nftables**: regla de rescate SSH; `DOCKER-USER` filtra el origen de 443; el instalador
  neutraliza los `flush ruleset` que dejarían a Docker sin red; publicados solo en IPv4.
- **Auditoría**: el socket de Docker solo lo ve `docker-socket-proxy` (solo lectura, solo `GET`,
  red interna); Vector, Loki y Grafana en `gd_audit`; Grafana con OIDC y sin formulario.
- **Instalación**: Docker y gVisor desde repositorios apt con clave; binario del release
  verificado por SHA-256 y, si hay cosign, por firma keyless del workflow; imágenes por digest.
- **Copias**: la frase de restic nunca toca el disco (`RESTIC_PASSWORD_COMMAND`); el área de
  preparación es 0700 y se borra; los volúmenes se leen con un contenedor sin red.
- **Historial de git**: gitleaks sobre los 43 commits no encuentra secretos (solo los dos
  valores de prueba documentados).

## Novedades de v0.4, revisadas

- **`guardianctl upgrade`**: nada descargado se ejecuta ni se extrae sobre el repo antes de
  verificarlo (SHA-256 en Go contra `SHA256SUMS`; firma keyless de cosign con la misma identidad
  que `install.sh`). La extracción rechaza rutas absolutas, `..`, enlaces que salgan del árbol,
  dispositivos y entradas de más de 64 MB; `VERSION` del tarball debe coincidir con el tag. No
  toca `guardian.yaml`, `compose/.env`, `vault/`, certificados ni manifiestos propios; los
  archivos que el administrador edita (`compose/gateway/config.yaml`, `nftables/*.nft`) nunca se
  sobrescriben (quedan como `.new`). Copia previa con restic y `.previous/` para deshacer.
  Requiere root (salvo `--check`/`--dry-run`) y `.upgrade/`/`.previous/` son 0700.
- **Migraciones** (`internal/migrate`): idempotentes, registradas en `compose/.migrated`; la
  0.3.0 reescribe `compose/.env` con modo 0600 y nunca borra volúmenes (los lista).
- **Modelos** (`guardianctl model`): lectura de solo lectura del volumen `ollama_data` desde el
  host; el digest fijado es el SHA-256 del manifiesto (lo mismo que muestra `ollama list`);
  `doctor` no lee GB cada 15 minutos (solo manifiestos y tamaños), `model verify` sí. Los
  eventos `model_check` entran a Vector por un `http_server` nuevo (8689) en `gd_audit`, con la
  misma confianza que el informe del doctor (ver riesgo aceptado de Vector).
- **`tmpfs:` en manifiestos**: directorios escribibles adicionales para el agente, siempre
  `nosuid,nodev,noexec`, uid 10000, modo 0700, tamaño acotado; rutas del sistema prohibidas
  (`/etc`, `/usr`, `/run/guardian`, …). No amplían lo que el agente puede tocar fuera de sí mismo.
- **Hermes Agent y OpenClaw**: imágenes fijadas por digest de índice multi-arch (verificadas
  contra Docker Hub y ghcr.io), sin egreso, llave LLM efímera. Los `run.sh` leen la llave del
  archivo de secretos dentro del sandbox; OpenClaw la recibe en una variable de entorno de su
  propio proceso (ver riesgo aceptado).

## Riesgos aceptados (y cómo mitigarlos si te preocupan)

| Riesgo | Por qué se acepta | Mitigación opcional |
|---|---|---|
| Vector acepta eventos en `:8687/llm` y `:8688/doctor` sin autenticación desde `gd_audit`. | Solo llegan gateway, Loki, Grafana y el proxy del socket; ninguno está expuesto. Un contenedor comprometido podría inyectar eventos falsos, no leer los reales. | Mover Vector a una red solo con el gateway; firmar los eventos (v0.4). |
| Loki sin autenticación en `gd_audit`. | Misma red interna; Grafana es el único lector previsto. | Habilitar `auth_enabled` con tenant y cabecera desde Grafana. |
| El contenedor de Caddy corre como root dentro de la imagen oficial (trivy DS-0002). | Necesita 443; ya va con `cap_drop ALL` + `NET_BIND_SERVICE` y `no-new-privileges`. | Imagen propia con usuario sin privilegios y `net.ipv4.ip_unprivileged_port_start`. |
| Los agentes se identifican por IP fija en `gd_agents`. | La red es interna y solo el daemon asigna IPs; suplantarla exige control del daemon (que ya es todo). | gVisor + autenticación de proxy por agente (v0.4). |
| `/admin` del gateway es alcanzable desde LAN y VPN (con master key). | Es lo que usa `guardianctl` a través de Caddy; hay límite de intentos y comparación constante. | Restringir `api.<DOMAIN>/admin/*` en Caddy a la IP del host. |
| `guardianctl` corre como root y lee su propio repo (gosec G304/G703 "path traversal"). | Las rutas salen de la configuración del administrador, no de entradas remotas; las que vienen de manifiestos ya están confinadas (hallazgo 2). | — |
| Pocket ID 2.14 se reinicia solo en VMs con saltos de reloj. | No es de seguridad; sesiones intactas. | Sincronización horaria del host. |
| `upgrade` sin cosign instalado solo verifica SHA-256 (igual que `install.sh`). | El SHA-256 viene del mismo release que el tarball: protege de corrupción, no de un GitHub comprometido. | `apt install cosign` y `upgrade --require-signature`. |
| `run.sh` de OpenClaw exporta la llave LLM como variable de entorno del proceso del agente. | Es efímera (1 día, revocada al salir), solo existe dentro del sandbox y no aparece en `docker inspect` (no la pasa guardianctl). OpenClaw no admite leerla de archivo en la versión fijada. | Usar `"$file:"` en `openclaw.json` cuando la versión fijada lo soporte. |
| Los agentes reales leen su configuración de un bind mount de `agents/examples/` de solo lectura. | Mismo mecanismo que `hello-agent`; el contenido lo controla el administrador. | — |

## Cómo repetir la revisión

```bash
go vet ./... && go test ./...
gosec -quiet ./...                      # revisa a mano las categorías excluidas en CI
staticcheck ./...
docker run --rm -v "$PWD:/mnt:ro" koalaman/shellcheck:stable -S style install.sh scripts/*.sh sandbox/runner.sh
docker run --rm -i hadolint/hadolint < compose/gateway/Dockerfile
docker run --rm -v "$PWD:/repo:ro" aquasec/trivy:latest config /repo
docker run --rm -v "$PWD:/repo:ro" zricethezav/gitleaks:latest detect --source /repo
sudo make doctor                        # 20 comprobaciones en el host
```

Y las pruebas de la VM que acompañan a cada commit: manifiestos con `mounts` maliciosos,
`docker.sock`, digests falsos, fuerza bruta contra `api.<DOMAIN>`, nodos caídos, presupuestos.
En v0.4, además: `go test ./...` cubre tarballs maliciosos de `upgrade` (traversal, enlaces
absolutos, dispositivos, suma o firma incorrectas), migraciones sobre un `.env` de v0.2 y blobs
de modelos alterados, truncados o ausentes.

## Informar de una vulnerabilidad

Abre un issue privado (GitHub → Security → Report a vulnerability) o escribe al autor antes de
publicarla. Se agradece un plazo razonable para corregir y publicar un release firmado.
