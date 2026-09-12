COMPOSE := docker compose --env-file compose/.env -f compose/docker-compose.yml

.PHONY: up down restart ps logs build doctor status nft-check

up:
	$(COMPOSE) up -d

down:
	$(COMPOSE) down

## restart: recrea open-webui para que relea el .env (OAUTH_*), y reinicia el resto.
restart:
	$(COMPOSE) up -d --force-recreate open-webui
	$(COMPOSE) restart caddy pocket-id wg-easy ollama

ps:
	$(COMPOSE) ps

logs:
	$(COMPOSE) logs -f --tail=100

build:
	go build -o bin/guardianctl ./cmd/guardianctl

doctor: build
	sudo ./bin/guardianctl doctor

status: build
	./bin/guardianctl status

## nft-check: valida la sintaxis del ruleset sin aplicarlo (regla dura 7).
nft-check:
	sudo nft -c -f nftables/guardian.nft
