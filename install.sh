#!/usr/bin/env bash
# Guardian v0.1 — instalador.
# Uso:  sudo ./install.sh [--with-nftables]
#
# Idempotente: se puede ejecutar varias veces. No usa `curl | bash` (regla dura 3):
# Docker se instala desde el repositorio apt oficial con keyring verificado.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_DIR="${REPO_DIR}/compose"
ENV_FILE="${COMPOSE_DIR}/.env"
CERT_DIR="${COMPOSE_DIR}/certs"
COMPOSE=(docker compose --env-file "${ENV_FILE}" -f "${COMPOSE_DIR}/docker-compose.yml")
WITH_NFTABLES=0

for arg in "$@"; do
	case "${arg}" in
		--with-nftables) WITH_NFTABLES=1 ;;
		-h|--help) sed -n '2,7p' "$0"; exit 0 ;;
		*) echo "Argumento desconocido: ${arg}" >&2; exit 2 ;;
	esac
done

log()  { printf '\033[1;34m[guardian]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[guardian] AVISO:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[guardian] ERROR:\033[0m %s\n' "$*" >&2; exit 1; }

require_root() {
	[[ "${EUID}" -eq 0 ]] || die "Ejecuta con sudo: sudo ./install.sh"
}

check_os() {
	[[ -r /etc/os-release ]] || die "No se encuentra /etc/os-release; solo se soporta Linux."
	# shellcheck disable=SC1091
	. /etc/os-release
	OS_ID="${ID:-}"
	OS_VERSION="${VERSION_ID:-}"
	OS_CODENAME="${VERSION_CODENAME:-}"
	case "${OS_ID}:${OS_VERSION}" in
		debian:12|ubuntu:24.04) log "Sistema soportado: ${PRETTY_NAME}" ;;
		debian:*|ubuntu:*) warn "Probado solo en Debian 12 y Ubuntu 24.04; tienes ${PRETTY_NAME}. Continúo." ;;
		*) warn "Distribución no soportada (${PRETTY_NAME}). Instala Docker manualmente si falla." ;;
	esac
}

install_docker() {
	if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
		log "Docker y compose ya instalados: $(docker --version)"
		return
	fi
	case "${OS_ID}" in
		debian|ubuntu) ;;
		*) die "Instala Docker Engine + compose plugin manualmente y vuelve a ejecutar." ;;
	esac
	log "Instalando Docker desde el repositorio oficial (apt + keyring)…"
	# Fuente: https://docs.docker.com/engine/install/${OS_ID}/ (formato deb822).
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq
	apt-get install -y -qq ca-certificates curl gnupg >/dev/null
	install -m 0755 -d /etc/apt/keyrings
	if [[ ! -s /etc/apt/keyrings/docker.asc ]]; then
		curl -fsSL "https://download.docker.com/linux/${OS_ID}/gpg" -o /etc/apt/keyrings/docker.asc
		chmod a+r /etc/apt/keyrings/docker.asc
	fi
	cat > /etc/apt/sources.list.d/docker.sources <<SRC
Types: deb
URIs: https://download.docker.com/linux/${OS_ID}
Suites: ${OS_CODENAME}
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
SRC
	apt-get update -qq
	apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
	systemctl enable --now docker
	log "Docker instalado: $(docker --version)"
}

gen_secret_if_empty() {
	# gen_secret_if_empty VAR "comando que imprime el secreto"
	local var="$1" cmd="$2" current
	current="$(grep -E "^${var}=" "${ENV_FILE}" | head -n1 | cut -d= -f2- || true)"
	if [[ -z "${current}" ]]; then
		local value
		value="$(eval "${cmd}")"
		sed -i "s|^${var}=.*|${var}=${value}|" "${ENV_FILE}"
		log "Generado ${var}."
	fi
}

ensure_env_var() {
	# Añade VAR= al .env si falta (instalaciones anteriores a la variable).
	grep -qE "^$1=" "${ENV_FILE}" || printf '%s=\n' "$1" >> "${ENV_FILE}"
}

ensure_guardianctl() {
	# Orden: binario ya presente (release tarball) → compilar con Go ≥ 1.22 → descargar del release
	# de GitHub con verificación SHA-256. Nunca `curl | bash`.
	local bin="${REPO_DIR}/bin/guardianctl" version arch
	version="$(cat "${REPO_DIR}/VERSION")"
	case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) die "arquitectura no soportada: $(uname -m)" ;; esac
	if [[ -x "${bin}" ]]; then
		log "guardianctl ya disponible: $("${bin}" version)"
		return
	fi
	if [[ -x "${REPO_DIR}/bin/guardianctl-linux-${arch}" ]]; then
		cp "${REPO_DIR}/bin/guardianctl-linux-${arch}" "${bin}"; chmod 0755 "${bin}"
		log "guardianctl tomado del tarball del release."
		return
	fi
	if command -v go >/dev/null 2>&1 && [[ "$(go version | grep -oE 'go1\.[0-9]+' | cut -d. -f2)" -ge 22 ]]; then
		log "Compilando guardianctl con $(go version | awk '{print $3}')…"
		(cd "${REPO_DIR}" && go build -ldflags "-s -w -X main.version=${version}" -o "${bin}" ./cmd/guardianctl)
		return
	fi
	local base="https://github.com/jovamcp/guardian/releases/download/v${version}"
	log "Descargando guardianctl v${version} (linux/${arch}) del release y verificando SHA-256…"
	mkdir -p "${REPO_DIR}/bin"
	curl -fsSL "${base}/guardianctl-linux-${arch}" -o "${bin}.tmp"
	curl -fsSL "${base}/SHA256SUMS" -o "${REPO_DIR}/bin/SHA256SUMS"
	local expected actual
	expected="$(awk -v f="guardianctl-linux-${arch}" '$2==f{print $1}' "${REPO_DIR}/bin/SHA256SUMS")"
	actual="$(sha256sum "${bin}.tmp" | awk '{print $1}')"
	[[ -n "${expected}" && "${expected}" == "${actual}" ]] || die "SHA-256 de guardianctl no coincide (esperado ${expected:-?}, obtenido ${actual})"
	mv "${bin}.tmp" "${bin}"; chmod 0755 "${bin}"
	log "guardianctl verificado e instalado en bin/."
}

run_init() {
	# guardian.yaml es la fuente de verdad de la red: pregunta (si hay terminal) o usa flags/defaults.
	local bin="${REPO_DIR}/bin/guardianctl"
	if [[ -f "${REPO_DIR}/guardian.yaml" ]]; then
		log "guardian.yaml ya existe; sincronizo .env y nftables con él."
		"${bin}" init --yes
	elif [[ -t 0 ]]; then
		"${bin}" init
	else
		warn "Sin terminal: guardian.yaml con valores por defecto. Ajusta con: sudo ./bin/guardianctl init --domain … --lan … --ai-cidr … --ai-host …"
		"${bin}" init --yes
	fi
	set -a
	# shellcheck disable=SC1090,SC1091
	. "${ENV_FILE}"
	set +a
}

prepare_env() {
	if [[ ! -f "${ENV_FILE}" ]]; then
		install -m 0600 "${REPO_DIR}/.env.example" "${ENV_FILE}"
		log "Creado ${ENV_FILE} (0600) a partir de .env.example."
	else
		chmod 0600 "${ENV_FILE}"
		log "${ENV_FILE} ya existe; conservo su contenido."
	fi
	# El .env pertenece al usuario que invocó sudo (si existe) para que pueda editarlo;
	# sigue siendo 0600. Los comandos de Docker se ejecutan con sudo (Docker rootful).
	if [[ -n "${SUDO_UID:-}" && -n "${SUDO_GID:-}" ]]; then
		chown "${SUDO_UID}:${SUDO_GID}" "${ENV_FILE}"
	fi
	command -v openssl >/dev/null 2>&1 || apt-get install -y -qq openssl >/dev/null
	gen_secret_if_empty WEBUI_SECRET_KEY "openssl rand -hex 32"
	gen_secret_if_empty POCKET_ID_ENCRYPTION_KEY "openssl rand -base64 32"
	ensure_env_var LITELLM_MASTER_KEY; gen_secret_if_empty LITELLM_MASTER_KEY "echo sk-\$(openssl rand -hex 24)"
	ensure_env_var LITELLM_SALT_KEY;  gen_secret_if_empty LITELLM_SALT_KEY "echo sk-\$(openssl rand -hex 24)"
	ensure_env_var LITELLM_DB_PASSWORD; gen_secret_if_empty LITELLM_DB_PASSWORD "openssl rand -hex 24"
	ensure_env_var GRAFANA_ADMIN_PASSWORD; gen_secret_if_empty GRAFANA_ADMIN_PASSWORD "openssl rand -hex 16"
	for v in GRAFANA_OAUTH_CLIENT_ID GRAFANA_OAUTH_CLIENT_SECRET LOKI_RETENTION_PERIOD NTFY_URL NTFY_TOPIC; do ensure_env_var "$v"; done

	set -a
	# shellcheck disable=SC1090,SC1091
	. "${ENV_FILE}"
	set +a
}

export_caddy_ca() {
	# Open WebUI monta compose/certs/root.crt, así que la CA debe existir ANTES de
	# levantar el resto. Se levanta solo caddy, se espera a que genere su PKI y se copia.
	mkdir -p "${CERT_DIR}"
	if [[ -s "${CERT_DIR}/root.crt" ]]; then
		log "CA interna ya exportada en ${CERT_DIR}/root.crt."
		return
	fi
	log "Levantando solo caddy para generar la CA interna…"
	"${COMPOSE[@]}" up -d caddy
	for _ in $(seq 1 30); do
		if docker exec gd-caddy test -s /data/caddy/pki/authorities/local/root.crt 2>/dev/null; then
			docker cp gd-caddy:/data/caddy/pki/authorities/local/root.crt "${CERT_DIR}/root.crt"
			chmod 0644 "${CERT_DIR}/root.crt"
			log "CA exportada a ${CERT_DIR}/root.crt."
			return
		fi
		sleep 2
	done
	die "Caddy no generó la CA en 60 s. Revisa: docker logs gd-caddy"
}

start_stack() {
	log "Levantando la plataforma…"
	"${COMPOSE[@]}" pull --quiet
	"${COMPOSE[@]}" up -d
	# El Caddyfile va montado: si cambió, `up` no reinicia caddy. Recarga en caliente.
	"${COMPOSE[@]}" exec -T caddy caddy reload --config /etc/caddy/Caddyfile >/dev/null 2>&1 || true
	"${COMPOSE[@]}" ps
}

apply_nftables() {
	[[ "${WITH_NFTABLES}" -eq 1 ]] || { log "nftables omitido (usa --with-nftables para aplicarlo)."; return; }
	command -v nft >/dev/null 2>&1 || apt-get install -y -qq nftables >/dev/null
	local dst=/etc/guardian/nftables
	install -d -m 0755 "${dst}"
	install -m 0644 "${REPO_DIR}/nftables/guardian.nft" "${dst}/guardian.nft"
	install -m 0644 "${REPO_DIR}/nftables/docker-user.nft" "${dst}/docker-user.nft"

	# Regla dura 7: nunca aplicar sin validar. Se valida el conjunto que quedará persistido.
	log "Validando rulesets (nft -c)…"
	nft -c -f "${dst}/guardian.nft"
	nft -c -f "${dst}/docker-user.nft"
	log "Aplicando tabla inet guardian (host: SSH de rescate, ICMP, log de descartes)…"
	nft -f "${dst}/guardian.nft"
	log "Aplicando DOCKER-USER (origen permitido para 443 y 51820)…"
	nft -f "${dst}/docker-user.nft"

	# Persistencia: include desde /etc/nftables.conf y servicio habilitado.
	local conf=/etc/nftables.conf line
	[[ -f "${conf}" ]] || printf '#!/usr/sbin/nft -f\n' > "${conf}"
	[[ -f "${conf}.guardian.bak" ]] || cp -a "${conf}" "${conf}.guardian.bak"
	# El archivo por defecto de Debian/Ubuntu empieza con `flush ruleset`: al (re)iniciar
	# nftables.service borraría también las tablas de Docker (NAT, FORWARD) y los contenedores
	# se quedarían sin red. Nuestros archivos ya vacían solo lo suyo, así que lo desactivamos.
	if grep -qE '^\s*flush ruleset' "${conf}"; then
		sed -i -E 's|^(\s*)flush ruleset|\1# flush ruleset  # desactivado por Guardian: borraría las reglas de Docker|' "${conf}"
		log "Desactivado 'flush ruleset' en ${conf} (copia en ${conf}.guardian.bak)."
	fi
	for line in "include \"${dst}/guardian.nft\"" "include \"${dst}/docker-user.nft\""; do
		grep -qxF "${line}" "${conf}" || printf '%s\n' "${line}" >> "${conf}"
	done
	nft -c -f "${conf}"
	# nftables.service de Debian/Ubuntu ejecuta `nft flush ruleset` en ExecStop, así que un
	# `systemctl restart nftables` también dejaría a Docker sin red. Con este drop-in, parar el
	# servicio solo retira la tabla de Guardian y deja DOCKER-USER limpia.
	local dropin=/etc/systemd/system/nftables.service.d
	install -d -m 0755 "${dropin}"
	cat > "${dropin}/guardian.conf" <<'UNIT'
# Instalado por Guardian (install.sh --with-nftables). No usa `flush ruleset`: borraría las
# tablas de Docker. Al parar, solo se retiran las reglas propias.
[Service]
ExecStop=
ExecStop=-/usr/sbin/nft delete table inet guardian
ExecStop=-/usr/sbin/nft flush chain ip filter DOCKER-USER
UNIT
	systemctl daemon-reload
	# Solo enable (sin --now): las reglas ya están aplicadas y arrancar el servicio ahora
	# volvería a ejecutar el archivo completo.
	systemctl enable nftables >/dev/null 2>&1
	log "Persistido en ${conf}; nftables.service habilitado para el arranque (drop-in sin flush ruleset)."
}

main() {
	require_root
	check_os
	install_docker
	prepare_env
	ensure_guardianctl
	run_init
	[[ -n "${DOMAIN:-}" ]] || die "DOMAIN vacío en ${ENV_FILE}."
	log "Dominio: ${DOMAIN}  (id.${DOMAIN}, api.${DOMAIN}, logs.${DOMAIN}, vpn.${DOMAIN})"
	export_caddy_ca
	start_stack
	apply_nftables
	cat <<NEXT

========================================================================
 Guardian está levantado. Siguientes pasos:
  1. DNS local: apunta ${DOMAIN}, id.${DOMAIN} y vpn.${DOMAIN} a la IP de este host.
  2. Instala la CA en tus dispositivos: ${CERT_DIR}/root.crt
  3. Pocket ID: https://id.${DOMAIN}/setup → crea el admin (passkey) y un cliente
     OIDC "open-webui" con callback https://${DOMAIN}/oauth/oidc/callback
  4. Copia OAUTH_CLIENT_ID / OAUTH_CLIENT_SECRET a ${ENV_FILE} y ejecuta: make restart
  5. WireGuard: https://vpn.${DOMAIN} → asistente (host: ${WG_HOST:-<WG_HOST>}, puerto 51820)
  6. Firewall perimetral: sudo ./bin/guardianctl policy render fortios|opnsense
     Firewall del host:   sudo ./install.sh --with-nftables
  7. Comprueba: sudo make doctor   (guía completa: docs/instalacion.md)
========================================================================
NEXT
}

main "$@"
