# Prompt de trabajo — Fase 2 (semanas 3–4): agentes

Derivado de DESIGN.md (hito 2). No hay prompt externo para esta fase: este archivo fija el alcance
antes de tocar código, igual que `01-fase-1.md` lo hizo para la Fase 1.

## Objetivo

Un agente de ejemplo arranca desde su manifiesto YAML, llama al LLM **solo** a través del gateway
(LiteLLM) con **su** llave virtual, **solo** alcanza los dominios de su allowlist a través del
proxy de egreso, no ve la LAN ni el socket de Docker, y sus secretos llegan cifrados y como
archivos `0400`.

## Tareas (un commit por tarea, en este orden)

1. **Plan y decisiones.** Este archivo, DESIGN.md (sección 11 → decisiones tomadas) y CLAUDE.md
   (fase actual). Decisiones nuevas:
   - **Sin `userns-remap` en v0.1.** Es un ajuste global del daemon: recrea `/var/lib/docker`
     bajo otro uid y "desaparecen" imágenes y volúmenes existentes, y wg-easy necesita el
     userns del host. Los agentes corren como uid/gid fijos no privilegiados (`10000:10000`),
     `cap-drop ALL`, `no-new-privileges`, seccomp y AppArmor. Remapeo/gVisor en v0.2.
   - **Identidad del agente en la red = IP fija en `gd_agents`.** Squid y Blocky aplican la
     allowlist por IP de origen. Más simple que auth de proxy y suficiente porque la red es
     interna y solo `guardianctl` asigna IPs.
   - **LiteLLM necesita Postgres** para llaves virtuales: se añade `gd-litellm-db` (solo `gd_ai`).
2. **Gateway LiteLLM.** Servicios `litellm` (+ `litellm-db`), `api.<DOMAIN>` en Caddy,
   `compose/litellm/config.yaml` con los modelos de Ollama, `guardianctl key create|list|delete`
   contra `/key/generate`. Verificar: llave con `models: [qwen2.5:0.5b]` responde en
   `/v1/chat/completions` y recibe 401/403 al pedir otro modelo o sin llave.
3. **Redes y egreso.** `gd_agents` (`internal: true`) y `gd_egress`; Blocky (DNS, deniega todo
   salvo allowlist por grupo de cliente) y Squid (solo `CONNECT` a `dstdomain` de la allowlist
   del agente). `guardianctl policy render egress` genera ambas configuraciones a partir de
   `agents/*.yaml`. Verificar con un contenedor de prueba en `gd_agents`: sin ruta directa a
   Internet ni a la LAN; dominio permitido → 200 vía proxy; no permitido → 403 y NXDOMAIN.
4. **Sandbox.** `sandbox/seccomp-agent.json` (perfil por defecto de Docker menos syscalls
   peligrosas), `sandbox/apparmor/guardian-agent`, `sandbox/runner.sh` y `guardianctl agent
   run <nombre>` (parser YAML mínimo en stdlib). Agente de ejemplo real `agents/examples/
   hello-agent.yaml` con imagen por digest que llama al LLM y a un dominio permitido.
5. **Vault.** `guardianctl secret set|get` con `age` (binario del sistema; llave en
   `vault/key.txt`, ignorada por git); `agent run` descifra `vault:` a un tmpfs y lo monta
   `0400` en `/run/guardian/secrets/<nombre>`; nunca como variable de entorno.
6. **Doctor y docs.** `doctor` comprueba: `gd_agents` es internal; litellm/squid/blocky en
   ejecución; un contenedor efímero en `gd_agents` no alcanza `1.1.1.1:443` ni la LAN;
   `/var/run/docker.sock` no aparece en ningún contenedor de agente. `docs/agentes.md`.

## Criterios de aceptación

- `sudo guardianctl agent run hello-agent` termina con éxito: obtiene una respuesta del modelo
  vía `http://litellm:4000` con su llave y descarga una URL de su allowlist vía proxy.
- El mismo agente falla (403/NXDOMAIN) contra un dominio fuera de su allowlist, no alcanza la
  IP del host ni `10.10.10.0/24`, y `docker inspect` muestra: sin capabilities, `no-new-privileges`,
  read-only, seccomp y AppArmor personalizados, sin `docker.sock`, uid 10000.
- Una llave virtual de otro agente no puede usar modelos fuera de su lista.
- `make doctor` en verde con las nuevas comprobaciones; los puertos publicados siguen siendo
  solo 443/tcp y 51820/udp.

## Restricciones

Las mismas de la Fase 1: nada nuevo publicado, secretos solo en `compose/.env` o `vault/`,
imágenes nuevas fijadas por versión (por digest antes del release), cada cambio de compose
cita la fuente, y si algo del diseño es inseguro o imposible se documenta y se propone
alternativa antes de seguir.
