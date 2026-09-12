# Instalación de Guardian (borrador)

> Borrador de la Fase 1. La versión completa (DNS local, instalar la CA en iOS/Android/Windows/macOS,
> primer login, cliente WireGuard con QR) se escribe en la tarea 6 de la Fase 1.

## Requisitos

- Mini-PC o servidor con Debian 12 o Ubuntu 24.04, con IP fija en la zona de IA.
- Acceso `sudo`.
- Un dominio local (por defecto `ai.home`) que puedas resolver en tu DNS local.

## Pasos

1. Clonar el repositorio:

   ```bash
   git clone https://github.com/jovamcp/guardian.git
   cd guardian
   ```

2. Ejecutar el instalador (sin nftables la primera vez):

   ```bash
   sudo ./install.sh
   ```

3. Configurar DNS local: `<DOMAIN>`, `id.<DOMAIN>` y `vpn.<DOMAIN>` → IP del host.
4. Instalar la CA `compose/certs/root.crt` en cada dispositivo.
5. Crear el admin y el cliente OIDC `open-webui` en `https://id.<DOMAIN>/setup`.
6. Copiar `OAUTH_CLIENT_ID` / `OAUTH_CLIENT_SECRET` a `compose/.env` y ejecutar `make restart`.
7. Configurar WireGuard en `https://vpn.<DOMAIN>` y añadir tu móvil con el QR.
8. Comprobar con `make doctor`.
