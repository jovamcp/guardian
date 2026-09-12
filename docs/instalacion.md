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
5. Crear el admin y el cliente OIDC `open-webui` en Pocket ID (ver abajo).
6. Copiar `OAUTH_CLIENT_ID` / `OAUTH_CLIENT_SECRET` a `compose/.env` y ejecutar `make restart`.
7. Configurar WireGuard en `https://vpn.<DOMAIN>` y añadir tu móvil con el QR.
8. Comprobar con `make doctor`.

## Cliente OIDC en Pocket ID (verificado con Pocket ID 2.14.0)

1. Abre `https://id.<DOMAIN>/setup`. Rellena usuario, correo y nombre; pulsa **Sign Up**.
   A continuación registra tu **passkey** (Face ID, Windows Hello, llave de seguridad…).
2. Ve a **Administration → OIDC Clients → Add OIDC Client**:
   - **Name**: `open-webui`
   - **Client Launch URL**: `https://<DOMAIN>/`
   - **Callback URLs** → *Add*: `https://<DOMAIN>/oauth/oidc/callback`
     (ruta fija de Open WebUI; el proveedor se llama `oidc`).
   - Pulsa **Save**.
3. En la pestaña **Allowed User Groups**: el cliente nace **restringido sin grupos**, así que nadie
   podría entrar. Pulsa **Unrestrict** (y confirma) o asigna un grupo con tus usuarios.
   Si lo olvidas, Pocket ID responde "You are not allowed to access this service".
4. En la pestaña **Credentials** pulsa **Add client secret** y copia el secreto (solo se muestra una vez).
5. Copia el **Client ID** (arriba, en la ficha del cliente) y el secreto a `compose/.env`:

   ```
   OAUTH_CLIENT_ID=<client id>
   OAUTH_CLIENT_SECRET=<client secret>
   ```

6. `make restart`. En `https://<DOMAIN>` aparece solo el botón **Continue with Pocket ID**:
   el formulario de contraseña está desactivado (`ENABLE_LOGIN_FORM=false`). El primer usuario
   que entra por OIDC se convierte en administrador de Open WebUI.

> `ENABLE_LOGIN_FORM` y `ENABLE_SIGNUP` son configuración persistente de Open WebUI: se leen del
> entorno solo en el primer arranque. Para volver a aplicar el `.env` después (por ejemplo, para
> recuperar el acceso), pon `ENABLE_PERSISTENT_CONFIG=false` temporalmente y haz `make restart`.
