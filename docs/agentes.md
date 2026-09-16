# Agentes en Guardian (Fase 2)

Un **agente** es un contenedor que ejecuta código con acceso a un modelo (por el gateway) y a
un puñado de servicios externos (por el proxy), y nada más. Guardian no sabe qué hace el agente
por dentro; lo que garantiza es lo que puede tocar por fuera.

## Qué ve un agente

| Recurso | Cómo llega | Quién lo limita |
|---|---|---|
| Modelo LLM | `http://gateway:4000/v1` con **su** llave virtual (archivo `/run/guardian/secrets/llm_key`) | gd-gateway: solo los `llm.models` del manifiesto; rpm y presupuesto opcionales |
| Internet | Solo HTTPS por el proxy `HTTPS_PROXY=http://172.28.30.3:3128` | Squid: solo `CONNECT` a los dominios de `egress.allow`; nada en claro, nada a IPs privadas |
| DNS | `172.28.30.53` (Blocky) | Solo resuelve los dominios de `egress.allow`; el resto `NXDOMAIN` |
| Secretos | Archivos `0400` en `/run/guardian/secrets/<servicio>_token` | Vault cifrado con `age`; nunca variables de entorno |
| LAN, otros contenedores, host, socket de Docker | **No** | Red `gd_agents` interna; firewall; sandbox |

El sandbox (`sandbox/README.md`): uid `10000`, raíz de solo lectura, `cap-drop ALL`,
`no-new-privileges`, seccomp y AppArmor propios, límites de CPU/memoria/PIDs.

## El manifiesto

```yaml
name: mi-agente                       # minúsculas, dígitos y guiones
image: ghcr.io/org/agente@sha256:…    # SIEMPRE por digest
llm:
  models: [qwen2.5:0.5b]              # nombres tal y como los tiene Ollama y compose/gateway/config.yaml
  key: auto                           # llave virtual efímera por ejecución (o vault:<ref>)
egress:
  allow: [api.github.com, "*.pypi.org"]   # dominios exactos o comodín de subdominios
services:
  github:
    url: https://api.github.com       # se expone como GITHUB_URL
    token: vault:mi-agente/github     # se expone como GITHUB_TOKEN_FILE (archivo 0400)
resources: {cpus: "1", memory: 512m, pids: 128}
schedule: {cron: ""}                  # reservado: la planificación llega en la Fase 3
command: ["python3", "/app/main.py"]  # opcional
mounts: ["./mi-agente:/app"]          # opcional; rutas relativas al manifiesto; siempre solo lectura
tmpfs: ["/opt/data:512m"]             # opcional (v0.4): directorios escribibles y efímeros (nosuid,nodev,noexec) para agentes que exigen escribir en rutas fijas
env: {LOG_LEVEL: info}                # opcional; solo valores NO secretos
network: {ip: 172.28.30.120}          # opcional; si falta se deriva del nombre
sandbox: {runtime: gvisor}            # opcional (v0.2): runc por defecto; gvisor requiere install.sh --with-gvisor
```

Guárdalo en `agents/<nombre>.yaml`. Los ejemplos viven en `agents/examples/`.

## Flujo de trabajo

```bash
# 1. Los modelos de Ollama se descubren solos (compose/gateway/config.yaml, models: ["*"]).
sudo make restart

# 2. (una vez) crea el vault y guarda los secretos que use el agente
sudo ./bin/guardianctl secret init
echo "ghp_xxx" | sudo ./bin/guardianctl secret set mi-agente/github

# 3. Genera y aplica las allowlists de Squid y Blocky para todos los manifiestos
sudo make render-egress

# 4. Ejecuta el agente (primer plano; Ctrl-C lo para)
sudo ./bin/guardianctl agent run mi-agente
sudo ./bin/guardianctl agent run mi-agente --dry-run   # solo muestra el docker run
```

Prueba de humo incluida: `sudo ./bin/guardianctl agent run hello-agent` (antes,
`echo dummy | sudo ./bin/guardianctl secret set hello-agent/github_token`). Comprueba LLM,
egreso permitido y bloqueado, ausencia de ruta directa y el secreto `0400`.

## Llaves para aplicaciones (no agentes)

Cualquier app compatible con OpenAI (IDE, script, n8n) puede usar `https://api.<DOMAIN>/v1`
con una llave virtual:

```bash
sudo ./bin/guardianctl key create --name n8n --models qwen2.5:0.5b --rpm 60 --budget 5
sudo ./bin/guardianctl key list
sudo ./bin/guardianctl key delete n8n
```

La app confía en la CA interna (`compose/certs/root.crt`) igual que el navegador. Detalles en `docs/gateway.md`.

## Cambiar la allowlist de un agente

Edita `egress.allow`, ejecuta `sudo make render-egress` y vuelve a lanzar el agente. Squid y
Blocky se reinician en un par de segundos; los agentes en marcha conservan su IP, así que la
nueva política les aplica al instante.

## Límites conocidos en v0.1

- **Identidad por IP fija** en `gd_agents`: solo `guardianctl` asigna IPs, y la red es interna,
  pero un proceso con acceso al daemon de Docker podría suplantarla. El daemon es el límite de
  confianza de toda la plataforma.
- **Sin `userns-remap`**: el uid 10000 del agente es el uid 10000 del host y no hay archivos
  del host con ese propietario. Para un aislamiento de kernel adicional usa `sandbox.runtime:
  gvisor` (v0.2): el agente corre sobre el kernel en espacio de usuario de gVisor.
- **`schedule.cron`** se materializa con `guardianctl agent schedule apply` (timers de systemd).
- El proxy no inspecciona el contenido TLS: controla **a dónde** habla el agente, no qué dice.
  La auditoría de contenido llega con Vector/Loki en la Fase 3.

## Agentes reales: Hermes Agent y OpenClaw (v0.4)

Los dos agentes del cliente objetivo de DESIGN.md §1 tienen manifiesto probado en
`agents/examples/`. Ambos ejecutan **una tarea de un turno** contra el modelo local a través de
gd-gateway, con llave efímera, sin salida a Internet y con todo su estado en tmpfs (desaparece
al terminar). No hacen falta cuentas ni credenciales externas.

| | Hermes Agent (Nous Research) | OpenClaw |
|---|---|---|
| Imagen | `docker.io/nousresearch/hermes-agent` v2026.9.14, por digest | `ghcr.io/openclaw/openclaw` 2026.9.4, por digest |
| Modo | `hermes chat -q "<tarea>"` | `openclaw agent --local --message "<tarea>"` |
| Modelo | `config.yaml` → `provider: custom`, `base_url: http://gateway:4000/v1` | `openclaw.json` → `models.providers.guardian` (`api: openai-completions`) |
| Llave LLM | `run.sh` la escribe en `$HERMES_HOME/.env` desde `/run/guardian/secrets/llm_key` | `run.sh` la exporta como `OPENCLAW_LLM_KEY` (solo dentro del sandbox) |
| Estado | `tmpfs: [/opt/data:512m, /run:16m]` (el wrapper fuerza `HOME=/opt/data`; el bootstrap escribe en `/run/s6`) | `tmpfs: [/home/agent/.openclaw:256m]` + `OPENCLAW_STATE_DIR` |
| Tarea | `env.HERMES_TASK` | `env.OPENCLAW_TASK` |
| Egreso | `allow: []` — todo intento se deniega y registra | `allow: []` + `OPENCLAW_OFFLINE=1`, `OPENCLAW_NO_AUTO_UPDATE=1` |

```bash
sudo make render-egress                          # tras añadir o cambiar manifiestos
sudo ./bin/guardianctl agent run hermes-agent    # ~2 GB de imagen la primera vez
sudo ./bin/guardianctl agent run openclaw
```

Probados en caliente el 2026-09-16 (VM Debian 12 arm64, `qwen2.5:0.5b`): Hermes responde en
~25 s y OpenClaw en ~17 s; ambos con `llm_request` auditada y sin egreso. Con un modelo de 0.5B
las respuestas son pobres: es una prueba del sandbox, no del agente.

Qué comprobar en el panel: en **Guardian · LLM** una `llm_request` del alias
`hermes-agent-<ts>` / `openclaw-<ts>`; en **Guardian · Egreso**, cero `egress_allowed` y, si el
agente intentó telemetría o actualizaciones, `egress_denied`/`dns_blocked` con su IP.

Para usarlos con herramientas (búsqueda web, GitHub…), añade los dominios a `egress.allow`, los
secretos al vault y las variables/archivos que cada agente espera; el sandbox no cambia.
Un agente permanente (gateway de OpenClaw o de Hermes con canales de mensajería) queda fuera de
`agent run`: no expone puertos y termina cuando termina el comando. Si lo necesitas, abre un
issue: es el candidato a `mode: service` de una versión futura.
