#!/bin/bash
# Lanzador de Hermes Agent en el sandbox de Guardian: prepara $HERMES_HOME (tmpfs efímero) con la
# configuración y la llave LLM (leída del archivo de secretos, nunca de una variable de docker) y
# ejecuta una tarea de un turno. Todo lo que Hermes escriba desaparece al terminar el contenedor.
set -euo pipefail
: "${HERMES_HOME:=/opt/data}"
secrets="${GUARDIAN_SECRETS_DIR:-/run/guardian/secrets}"
model="${GUARDIAN_MODEL:-qwen2.5:0.5b}"
task="${HERMES_TASK:-Responde en una frase: ¿qué es Guardian?}"

mkdir -p "${HERMES_HOME}"
sed "s|__MODEL__|${model}|" /opt/guardian-agent/config.yaml > "${HERMES_HOME}/config.yaml"
umask 077
printf 'OPENAI_API_KEY=%s\n' "$(cat "${secrets}/llm_key")" > "${HERMES_HOME}/.env"
echo "[guardian] hermes chat -q (modelo ${model}) …" >&2
exec hermes chat -q "${task}"
