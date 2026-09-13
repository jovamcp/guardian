# gd-gateway: imagen propia construida en local desde bases fijadas por digest (regla dura 4).
# Contexto de build: la raíz del repo (ver compose/docker-compose.yml).
# Renovar digests: docker buildx imagetools inspect golang:1.22-alpine / gcr.io/distroless/static-debian12:nonroot
FROM golang:1.22-alpine@sha256:1699c10032ca2582ec89a24a1312d986a3f094aed3d5c1147b19880afe40e052 AS build
WORKDIR /src
COPY go.mod ./
COPY internal ./internal
COPY cmd/gd-gateway ./cmd/gd-gateway
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /gd-gateway ./cmd/gd-gateway \
 && mkdir -p /out/data && echo "datos de gd-gateway (keys.json, usage.json)" > /out/data/README \
 && chown -R 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /gd-gateway /gd-gateway
# /data (no vacío) con propietario nonroot: Docker copia el contenido y el propietario al crear el
# volumen, así el proceso puede escribir keys.json/usage.json sin correr como root.
COPY --from=build --chown=65532:65532 /out/data /data
USER nonroot:nonroot
EXPOSE 4000
ENTRYPOINT ["/gd-gateway"]
CMD ["--config", "/etc/gateway/config.yaml"]
