# Caddy con módulos DNS para el modo tls.mode: acme-dns (dominio real con DNS challenge).
# Bases fijadas por digest (regla dura 4). Se construye en local: no se publica ninguna imagen.
# Renovar digests: docker buildx imagetools inspect caddy:2-builder / caddy:2
FROM caddy:2-builder@sha256:403d237d0bb16d2e62b1f93ca9ebb4953ecbb78aee5986765713a39f9263a5b4 AS builder
RUN xcaddy build \
    --with github.com/caddy-dns/cloudflare \
    --with github.com/caddy-dns/duckdns

FROM caddy:2@sha256:13ba145cba2f3e28fa801994876e4c086d1b95d5aa2a520a734765ffb6b12017
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
