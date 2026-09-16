# Guardian

Capa de seguridad y red para IA local y agentes en homelab. Se instala **delante** de tu runtime de
IA (Ollama, llama.cpp, vLLM; también sobre Umbrel o ZimaOS) y añade lo que ninguno trae de serie:

- **Identidad**: login con OIDC y passkeys (Pocket ID).
- **Acceso remoto** solo por WireGuard; ningún puerto de aplicación abierto a Internet.
- **Zonificación** con nftables y plantillas para tu firewall (FortiOS, OPNsense y UniFi).
- **Gateway LLM propio** compatible con OpenAI (gd-gateway, Go) con llaves virtuales, varios nodos Ollama y
  cloud burst con presupuesto.
- **Sandbox de agentes** con egreso controlado por allowlist por agente (Squid + Blocky), secretos
  cifrados con `age` inyectados como archivos y perfil seccomp/AppArmor propio.
- **Auditoría** centralizada: Vector → Loki → Grafana con login OIDC, paneles de accesos, LLM y
  egreso, y alertas al móvil por ntfy. Agentes programados con timers de systemd.

No sustituye el runtime ni el chat: los protege. Diseño completo en [`DESIGN.md`](DESIGN.md).

## Estado

**v0.3.0 publicado; v0.4 en `main`** (`guardianctl upgrade` con releases firmados, migraciones y
rollback; verificación y fijación de modelos de Ollama; manifiestos reales de Hermes Agent y
OpenClaw), probado en VMs de Debian 12 y Ubuntu 24.04. Buscamos
[beta testers](docs/beta.md) con hardware real. Funciona: Caddy con TLS interno, Pocket ID (passkeys), Open WebUI con login OIDC,
Ollama aislado, wg-easy 15, nftables con persistencia, gd-gateway con llaves virtuales en
`api.<DOMAIN>`, red interna de agentes con Squid + Blocky por allowlist, sandbox
(seccomp/AppArmor/uid 10000/solo lectura), vault con `age`, auditoría en `logs.<DOMAIN>` (Loki + Grafana con OIDC, alertas ntfy) y
`guardianctl` (`init status doctor key policy agent secret backup upgrade migrate model`), `guardian.yaml`, políticas para
FortiOS, OPNsense y UniFi, copias con restic, gVisor opcional, imágenes por digest y releases firmados con cosign. Pendiente de prueba en hardware real
x86-64 y desde un móvil fuera de casa. Guías: [`docs/gateway.md`](docs/gateway.md), [`docs/agentes.md`](docs/agentes.md),
[`docs/auditoria.md`](docs/auditoria.md), [`docs/actualizacion.md`](docs/actualizacion.md), [`docs/modelos.md`](docs/modelos.md),
[`docs/proxmox-lxc.md`](docs/proxmox-lxc.md).
Revisión de seguridad y riesgos aceptados: [`docs/seguridad.md`](docs/seguridad.md).

## Instalación rápida

Requisitos: Debian 12 o Ubuntu 24.04, `sudo`, IP fija en la zona de IA.

```bash
# Release (recomendado): tarball + SHA256SUMS desde https://github.com/jovamcp/guardian/releases
sha256sum -c --ignore-missing SHA256SUMS && tar -xzf guardian-*.tar.gz && cd guardian-*/
# o: sudo git clone https://github.com/jovamcp/guardian.git /opt/guardian && cd /opt/guardian
sudo ./install.sh              # añade --with-nftables para aplicar el firewall del host
```

El instalador instala Docker desde el repositorio oficial de Docker (nunca `curl | bash`), crea
`compose/.env` con secretos generados, te hace cinco preguntas (`guardianctl init` →
`guardian.yaml`), exporta la CA interna de Caddy a `compose/certs/root.crt` y levanta la
plataforma. Solo se publican **443/tcp** y **51820/udp**. Sin terminal (`< /dev/null`) usa los
valores por defecto; ajusta después con `sudo ./bin/guardianctl init --domain … --lan … --ai-cidr …`.

## Después de instalar

1. **DNS local**: apunta `<DOMAIN>`, `id.`, `api.`, `logs.` y `vpn.<DOMAIN>` a la IP del host
   (por defecto `DOMAIN=ai.home`).
2. **Instala la CA** `compose/certs/root.crt` en cada dispositivo (ver `docs/instalacion.md`).
3. **Pocket ID**: entra en `https://id.<DOMAIN>/setup`, crea el usuario admin con passkey y un
   cliente OIDC llamado `open-webui` con callback `https://<DOMAIN>/oauth/oidc/callback`.
   En *Allowed User Groups* pulsa **Unrestrict** (el cliente nace restringido) y en
   *Credentials* crea un secreto.
4. Copia el **ID y el secreto** del cliente a `OAUTH_CLIENT_ID` / `OAUTH_CLIENT_SECRET` en `compose/.env`.
5. `sudo make restart` y entra en `https://<DOMAIN>` con **Continue with Pocket ID**. El primer
   usuario es administrador.
6. **WireGuard**: `https://vpn.<DOMAIN>` → asistente inicial → añade tu móvil con el QR.
7. **Firewall perimetral**: `sudo ./bin/guardianctl policy render fortios` (u `opnsense`) y pega
   la salida en tu firewall; en el host, `sudo ./install.sh --with-nftables`.
8. `sudo make doctor` debe estar todo en verde.

Guía paso a paso (DNS local, instalar la CA en iOS/Android/Windows/macOS, WireGuard):
[`docs/instalacion.md`](docs/instalacion.md).

## Estructura

```
cmd/guardianctl/   CLI (Go, stdlib): init, status, doctor, key, policy, agent, secret
compose/           docker-compose.yml, caddy/, gateway/, squid/, blocky/, vector/, loki/, grafana/, certs/
cmd/gd-gateway/    gateway LLM (Go, stdlib)
nftables/          firewall del host
policies/          plantillas del firewall perimetral (fortios, opnsense, unifi)
sandbox/           perfiles seccomp/AppArmor y runner de agentes
agents/            manifiestos de agentes (examples/hello-agent es la prueba de humo)
docs/              instalación y prompts de trabajo por fase
```

## Licencia

El núcleo de Guardian se distribuye bajo la **GNU Affero General Public License v3.0**.
Texto completo en [`LICENSE`](LICENSE). El tier Pro es propietario y vive en otro repositorio.
Contribuciones bajo DCO (`git commit -s`). "Guardian" es un nombre provisional.
