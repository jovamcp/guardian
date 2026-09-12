# Guardian

Capa de seguridad y red para IA local y agentes en homelab. Se instala **delante** de tu runtime de
IA (Ollama, llama.cpp, vLLM; también sobre Umbrel o ZimaOS) y añade lo que ninguno trae de serie:

- **Identidad**: login con OIDC y passkeys (Pocket ID).
- **Acceso remoto** solo por WireGuard; ningún puerto de aplicación abierto a Internet.
- **Zonificación** con nftables y plantillas para tu firewall (FortiOS; OPNsense y UniFi pendientes).
- **Sandbox de agentes** con egreso controlado por allowlist (Fase 2).
- **Auditoría** centralizada (Fase 3).

No sustituye el runtime ni el chat: los protege. Diseño completo en [`DESIGN.md`](DESIGN.md).

## Estado

**v0.1 en desarrollo, Fase 1 (base) completada y probada en VMs de Debian 12 y Ubuntu 24.04.**
Funciona: Caddy con TLS interno, Pocket ID (passkeys), Open WebUI con login OIDC y sin formulario
de contraseña, Ollama aislado en su red Docker, wg-easy 15, `install.sh` idempotente, nftables
(host + `DOCKER-USER`) con persistencia y `guardianctl init/status/doctor`.
Aún no: agentes, gateway LiteLLM, proxy de egreso, auditoría. Pendiente de prueba en hardware
real x86-64 y desde un móvil fuera de casa.

## Instalación rápida

Requisitos: Debian 12 o Ubuntu 24.04, `sudo`, IP fija en la zona de IA.

```bash
git clone https://github.com/jovamcp/guardian.git
cd guardian
sudo ./install.sh              # añade --with-nftables para aplicar el firewall del host
```

El instalador instala Docker desde el repositorio oficial de Docker (nunca `curl | bash`), crea
`compose/.env` con secretos generados, exporta la CA interna de Caddy a `compose/certs/root.crt`
y levanta la plataforma. Solo se publican **443/tcp** y **51820/udp**.

## Después de instalar

1. **DNS local**: apunta `<DOMAIN>`, `id.<DOMAIN>` y `vpn.<DOMAIN>` a la IP del host
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
7. `sudo make doctor` debe estar todo en verde.

Guía paso a paso (DNS local, instalar la CA en iOS/Android/Windows/macOS, WireGuard):
[`docs/instalacion.md`](docs/instalacion.md).

## Estructura

```
cmd/guardianctl/   CLI (Go, stdlib): status, doctor, version
compose/           docker-compose.yml, caddy/Caddyfile, certs/ (CA exportada)
nftables/          firewall del host
policies/          plantillas del firewall perimetral (fortios, opnsense, unifi)
sandbox/           perfiles seccomp/AppArmor y runner de agentes (Fase 2)
agents/examples/   manifiestos de agentes
docs/              instalación y prompts de trabajo por fase
```

## Licencia

El núcleo de Guardian se distribuye bajo la **GNU Affero General Public License v3.0**.
Texto completo en [`LICENSE`](LICENSE). El tier Pro es propietario y vive en otro repositorio.
Contribuciones bajo DCO (`git commit -s`). "Guardian" es un nombre provisional.
