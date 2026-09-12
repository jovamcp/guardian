# Docker es rootful (ver CLAUDE.md): si no somos root, anteponemos sudo.
SUDO    := $(if $(filter 0,$(shell id -u)),,sudo)
COMPOSE := $(SUDO) docker compose --env-file compose/.env -f compose/docker-compose.yml

.PHONY: up down restart reload-caddy ps logs build doctor status nft-check nft-apply

up:
	$(COMPOSE) up -d

down:
	$(COMPOSE) down

## restart: recrea open-webui para que relea el .env (OAUTH_*), reinicia el resto y recarga Caddy.
restart:
	$(COMPOSE) up -d --force-recreate open-webui
	$(COMPOSE) restart pocket-id wg-easy ollama litellm
	$(COMPOSE) exec -T caddy caddy reload --config /etc/caddy/Caddyfile

## reload-caddy: recarga el Caddyfile sin cortar conexiones.
reload-caddy:
	$(COMPOSE) exec -T caddy caddy reload --config /etc/caddy/Caddyfile

ps:
	$(COMPOSE) ps

logs:
	$(COMPOSE) logs -f --tail=100

build:
	go build -o bin/guardianctl ./cmd/guardianctl

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
