#!/bin/sh
# Lanzador de OpenClaw en el sandbox de Guardian: copia la plantilla de configuración (openclaw.json,
# esquema de https://docs.openclaw.ai/concepts/model-providers/custom-providers; sin claves extra:
# OpenClaw rechaza claves desconocidas como "$comment") al
# directorio de estado (tmpfs efímero), toma la llave LLM del archivo de secretos y ejecuta un
# turno local del agente. Nada persiste al terminar el contenedor.
set -eu
secrets="${GUARDIAN_SECRETS_DIR:-/run/guardian/secrets}"
model="${GUARDIAN_MODEL:-qwen2.5:0.5b}"
task="${OPENCLAW_TASK:-Responde en una frase: ¿qué es Guardian?}"
state="${OPENCLAW_STATE_DIR:-/home/agent/.openclaw}"

mkdir -p "${state}" /home/agent/workspace
sed "s|__MODEL__|${model}|g" /opt/guardian-agent/openclaw.json > "${state}/openclaw.json"
OPENCLAW_LLM_KEY="$(cat "${secrets}/llm_key")"
export OPENCLAW_LLM_KEY
echo "[guardian] openclaw agent --local (modelo ${model}) …" >&2
cd /app
exec node openclaw.mjs agent --local --agent main --message "${task}"
