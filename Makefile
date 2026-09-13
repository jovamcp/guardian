# Docker es rootful (ver CLAUDE.md): si no somos root, anteponemos sudo.
SUDO    := $(if $(filter 0,$(shell id -u)),,sudo)
# compose/.extra-files lo escribe guardianctl init (p. ej. "-f compose/tls-acme-dns.yml").
COMPOSE := $(SUDO) docker compose --env-file compose/.env -f compose/docker-compose.yml $(shell cat compose/.extra-files 2>/dev/null)

.PHONY: up down restart reload-caddy render-egress restart-egress release pin-images pin-check ps logs build doctor status nft-check nft-apply

up:
	$(COMPOSE) up -d

down:
	$(COMPOSE) down

## restart: recrea open-webui para que relea el .env (OAUTH_*), reinicia el resto y recarga Caddy.
restart:
	$(COMPOSE) up -d --force-recreate open-webui
	$(COMPOSE) restart pocket-id wg-easy ollama litellm
	$(COMPOSE) exec -T caddy caddy reload --config /etc/caddy/Caddyfile

## render-egress: regenera las allowlists de Squid y Blocky desde agents/*.yaml y las aplica.
render-egress: build
	$(SUDO) ./bin/guardianctl policy render egress
	$(MAKE) restart-egress

restart-egress:
	$(COMPOSE) restart squid blocky

## reload-caddy: recarga el Caddyfile sin cortar conexiones.
reload-caddy:
	$(COMPOSE) exec -T caddy caddy reload --config /etc/caddy/Caddyfile

ps:
	$(COMPOSE) ps

logs:
	$(COMPOSE) logs -f --tail=100

VERSION := $(shell cat VERSION)
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/guardianctl ./cmd/guardianctl

## release: tarball reproducible + binarios linux/amd64 y arm64 + SHA256SUMS en dist/.
release: pin-check
	rm -rf dist && mkdir -p dist/guardian-$(VERSION)/bin
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/guardianctl-linux-amd64 ./cmd/guardianctl
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/guardianctl-linux-arm64 ./cmd/guardianctl
	git archive --format=tar HEAD | tar -x -C dist/guardian-$(VERSION)
	cp dist/guardianctl-linux-amd64 dist/guardianctl-linux-arm64 dist/guardian-$(VERSION)/bin/
	tar -C dist -czf dist/guardian-$(VERSION).tar.gz guardian-$(VERSION)
	rm -rf dist/guardian-$(VERSION)
	cd dist && if command -v sha256sum >/dev/null 2>&1; then sha256sum guardian-$(VERSION).tar.gz guardianctl-linux-amd64 guardianctl-linux-arm64 > SHA256SUMS; else shasum -a 256 guardian-$(VERSION).tar.gz guardianctl-linux-amd64 guardianctl-linux-arm64 > SHA256SUMS; fi
	@echo "release en dist/ (sube el tarball, los binarios y SHA256SUMS al release v$(VERSION))"

## pin-images: fija las imágenes del compose por digest (regla dura 4). pin-check solo comprueba.
pin-images:
	scripts/pin-images.sh

pin-check:
	scripts/pin-images.sh --check

doctor: build
	$(SUDO) ./bin/guardianctl doctor

status: build
	$(SUDO) ./bin/guardianctl status

## nft-check: valida la sintaxis de los rulesets sin aplicarlos (regla dura 7).
nft-check:
	$(SUDO) nft -c -f nftables/guardian.nft
	$(SUDO) nft -c -f nftables/docker-user.nft

## nft-apply: valida y reaplica (idempotente). No usa systemctl restart nftables.
nft-apply: nft-check
	$(SUDO) nft -f nftables/guardian.nft
	$(SUDO) nft -f nftables/docker-user.nft
