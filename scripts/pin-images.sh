#!/usr/bin/env bash
# Fija cada `image:` de compose/docker-compose.yml al digest del índice multi-arch (regla dura 4).
# Uso: scripts/pin-images.sh [--check]
#   --check  solo comprueba que todas las imágenes llevan @sha256 (para CI); no modifica nada.
# Requiere docker con buildx (docker buildx imagetools inspect).
set -euo pipefail
COMPOSE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/compose/docker-compose.yml"
if [[ "${1:-}" == "--check" ]]; then
	missing=$(grep -E '^[[:space:]]+image:' "${COMPOSE}" | grep -v '@sha256:' || true)
	if [[ -n "${missing}" ]]; then
		echo "imágenes sin digest:"; echo "${missing}"; exit 1
	fi
	echo "todas las imágenes llevan digest"; exit 0
fi
tmp="$(mktemp)"; trap 'rm -f "${tmp}"' EXIT
cp "${COMPOSE}" "${tmp}"
while IFS= read -r line; do
	ref=$(sed -E 's/^[[:space:]]+image:[[:space:]]*//; s/[[:space:]]+#.*$//' <<<"${line}")
	base="${ref%%@*}"
	# Sin `exit` en awk: cerraría la tubería antes de tiempo (SIGPIPE en docker + set -e).
	digest=$(docker buildx imagetools inspect "${base}" 2>/dev/null </dev/null | awk '/^Digest:/{d=$2} END{print d}')
	if [[ -z "${digest}" ]]; then
		echo "no se pudo resolver ${base}" >&2; exit 1
	fi
	new="${base}@${digest}"
	if [[ "${ref}" != "${new}" ]]; then
		echo "${base} → ${digest}"
		# Reemplaza solo la línea exacta.
		python3 - "${tmp}" "${line}" "${new}" <<'PY'
import sys,re
path,line,new=sys.argv[1:]
s=open(path).read()
indent=re.match(r'^(\s+)image:',line).group(1)
s=s.replace(line+"\n", f"{indent}image: {new}\n",1)
open(path,"w").write(s)
PY
	fi
done < <(grep -E '^[[:space:]]+image:' "${COMPOSE}")
cp "${tmp}" "${COMPOSE}"
echo "compose fijado por digest: ${COMPOSE}"
