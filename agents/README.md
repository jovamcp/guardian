# agents/

Manifiestos de agentes (uno por archivo, `nombre.yaml`). Formato en `DESIGN.md` §5 y ejemplos en
`examples/`. `guardianctl policy render egress` lee `agents/*.yaml` y `agents/examples/*.yaml`
para generar las allowlists de Squid y Blocky; `guardianctl agent run <nombre>` lanza el agente
con el perfil de sandbox de `sandbox/`.

Campos: `name`, `image` (por digest), `llm.models`, `llm.key` (`auto` o `vault:`), `egress.allow`,
`services.<n>.{url,token: vault:…}`, `resources.{cpus,memory,pids}`, `schedule.cron`, y opcionales
`command`, `mounts` (`origen:destino`, siempre solo lectura), `tmpfs` (`ruta[:tamaño]`, escribible y efímero),
`env`, `network.ip`, `sandbox.runtime`.
