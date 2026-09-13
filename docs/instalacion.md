# Instalación de Guardian v0.1 (Fase 1)

Esta guía está pensada para alguien que tiene un mini-PC en casa y quiere usar su IA local con
seguridad. No hace falta ser experto: cada paso explica qué hace y por qué. Si algo falla,
mira la sección [Problemas frecuentes](#problemas-frecuentes) al final.

Al terminar tendrás:

- **Open WebUI** (el chat) en `https://<DOMAIN>`, con login mediante **Pocket ID** (passkeys:
  Face ID, huella, Windows Hello o llave física). Sin contraseñas.
- **Ollama** (los modelos) accesible **solo** desde Open WebUI. Nadie en tu red puede hablar con
  Ollama directamente.
- **WireGuard** para entrar desde fuera de casa (móvil, portátil) sin abrir ningún otro puerto.
- El host solo escucha en **443/tcp** y **51820/udp** (y 22/tcp para SSH desde tu LAN).

Los ejemplos usan `DOMAIN=ai.home`. Sustitúyelo por el tuyo.

## 1. Requisitos

| Qué | Detalle |
|---|---|
| Sistema | Debian 12 o Ubuntu 24.04 Server, recién instalados o sin Docker previo |
| Hardware | 4 GB de RAM mínimo (8 GB recomendado); GPU opcional |
| Red | IP fija para el host (por DHCP reservado o estática). Ejemplo: `10.20.0.10` |
| Acceso | Usuario con `sudo` y acceso por SSH o teclado |
| DNS local | Poder crear tres nombres en tu router, Pi-hole, AdGuard, etc. (ver paso 3) |
| Acceso remoto | Un nombre DNS público o IP fija de tu conexión y poder abrir **51820/udp** en el router |

> **Importante**: Guardian instala Docker "rootful" (los contenedores los gestiona root).
> WireGuard necesita capacidades de red que Docker rootless no ofrece. Por eso los comandos
> `make …` piden `sudo`.

## 2. Instalar

```bash
sudo apt update && sudo apt install -y git
git clone https://github.com/jovamcp/guardian.git
cd guardian
sudo ./install.sh
```

El instalador:

1. Instala Docker desde el **repositorio oficial de Docker** (con clave GPG verificada; nunca
   `curl | bash`).
2. Crea `compose/.env` (permisos `0600`) y genera dos secretos: `WEBUI_SECRET_KEY` y
   `POCKET_ID_ENCRYPTION_KEY`. **Guarda una copia de `compose/.env`**: si pierdes la clave de
   Pocket ID, pierdes sus datos.
3. Ejecuta `guardianctl init`, que te pregunta (Enter acepta el valor entre corchetes):
   - **Dominio base**: el nombre que usarás en casa, por ejemplo `ai.home`.
   - **Zona horaria**: `America/Puerto_Rico`, `Europe/Madrid`…
   - **Red LAN**, **red de la zona de IA**, **IP de este host** y **VLAN**: si no tienes VLAN,
     pon la misma red que tu LAN y la IP real del host.
   - **Nombre DNS público o IP para WireGuard**: cómo se llega a tu casa desde Internet.
   - **Firewall perimetral** y, opcionalmente, servidor y topic de **ntfy** para las alertas.

   Todo queda en `guardian.yaml`; de ahí salen `compose/.env` (valores no secretos) y las redes
   del firewall del host. Puedes repetirlo cuando quieras: `sudo ./bin/guardianctl init`.
4. Arranca **solo Caddy**, espera a que cree su autoridad certificadora (CA) interna y la copia
   a `compose/certs/root.crt`. Este archivo es el que instalarás en tus dispositivos.
5. Levanta el resto de servicios y muestra los siguientes pasos.

Primera ejecución: 2–3 minutos más la descarga de imágenes (unos 5 GB). Ejecutarlo de nuevo
es seguro: no toca lo que ya existe.

### ¿Y el firewall del host?

Añade `--with-nftables` cuando ya tengas SSH funcionando desde tu LAN:

```bash
sudo ./install.sh --with-nftables
```

Aplica dos conjuntos de reglas, siempre validados antes con `nft -c`:

- `nftables/guardian.nft`: el host solo acepta SSH desde tu LAN y la zona de IA (**regla de
  rescate**, nunca se elimina), ICMP y lo que ya esté establecido. El resto se registra
  (`gd-drop-in:`) y se descarta.
- `nftables/docker-user.nft`: Docker publica los puertos por la cadena FORWARD, no por INPUT,
  así que las restricciones de **origen** para 443 y 51820 viven en la cadena `DOCKER-USER`:
  443 solo desde LAN, zona de IA y WireGuard; 51820 desde cualquier sitio (es el único
  servicio expuesto a Internet).

Las reglas se persisten en `/etc/nftables.conf` (mediante `include`) y `nftables.service`
queda habilitado. El instalador desactiva el `flush ruleset` global del archivo por defecto
y del `ExecStop` del servicio porque borrarían las tablas de Docker y dejarían los contenedores
sin red. Para reaplicar tras editar las reglas: `make nft-apply`.

Las redes salen de `guardian.yaml` (`guardianctl init` las escribe en ambos archivos con
`policy render nftables`), así que no hace falta editarlas a mano.

### El firewall perimetral (FortiGate, OPNsense)

```bash
sudo ./bin/guardianctl policy render fortios --out fortigate.txt    # CLI lista para pegar
sudo ./bin/guardianctl policy render opnsense --out opnsense.md     # guía paso a paso
```

Crean la VLAN de la zona de IA, permiten LAN → Guardian solo por 443, bloquean IA → LAN con
registro, limitan IA → Internet a HTTPS/DNS/NTP y redirigen 51820/udp al host. Revisa los nombres
de interfaz en `guardian.yaml` (`firewall:`) antes de aplicarlo.

## 3. DNS local

Tus dispositivos tienen que resolver tres nombres a la IP del host (ejemplo `10.20.0.10`):

| Nombre | Servicio |
|---|---|
| `ai.home` | Open WebUI (chat) |
| `id.ai.home` | Pocket ID (login) |
| `api.ai.home` | Gateway LLM para apps y agentes |
| `logs.ai.home` | Grafana (auditoría) |
| `vpn.ai.home` | Panel de WireGuard |

Dónde se configura, según lo que uses en casa:

- **Pi-hole**: *Local DNS → DNS Records*, añade los tres nombres con la IP.
- **AdGuard Home**: *Filtros → Reescrituras DNS*.
- **OPNsense**: *Services → Unbound DNS → Overrides → Host Overrides*.
- **FortiGate**: *Network → DNS Servers → DNS Database* (zona `home`, tipo *shadow*).
- **UniFi**: *Settings → Routing → DNS* (según versión) o usa Pi-hole/AdGuard.
- **Router doméstico sin esa opción**: usa Pi-hole o AdGuard como DNS de la red.

Comprueba desde otro equipo de la LAN:

```bash
nslookup ai.home
nslookup id.ai.home
```

> Sin acceso al DNS de la red, como apaño puedes editar el archivo `hosts` de cada equipo
> (`/etc/hosts` en Linux/macOS, `C:\Windows\System32\drivers\etc\hosts` en Windows).
> Los móviles no permiten esto sin root: ahí necesitas DNS de red o la VPN (paso 7).

## 4. Instalar la CA en tus dispositivos

Caddy emite certificados con su propia CA interna. Para que el navegador no avise, instala
`compose/certs/root.crt` una vez en cada dispositivo. **Solo instala este archivo, que has
generado tú.** Cópialo con `scp`, un pendrive, AirDrop, correo a ti mismo… (no es secreto:
es solo la parte pública).

```bash
# desde tu portátil
scp usuario@10.20.0.10:guardian/compose/certs/root.crt .
```

### iPhone / iPad (iOS 17 o superior)

1. Envíate el archivo (AirDrop, Archivos, correo) y ábrelo. iOS dirá *Perfil descargado*.
2. **Ajustes → General → VPN y gestión de dispositivos → Perfil descargado → Instalar**.
3. Después, **Ajustes → General → Información → Ajustes de confianza de los certificados**
   y activa el interruptor de **Caddy Local Authority**. Sin este último paso Safari seguirá
   avisando.

### Android (12 o superior)

1. Copia `root.crt` al teléfono.
2. **Ajustes → Seguridad y privacidad → Más ajustes de seguridad → Cifrado y credenciales →
   Instalar un certificado → Certificado CA**. Acepta el aviso y elige el archivo.
   (El nombre exacto de los menús cambia según el fabricante; busca "certificado CA" en Ajustes.)
3. Chrome lo usará automáticamente. Firefox para Android tiene su propio almacén:
   en **Ajustes → Acerca de Firefox** toca el logo 5 veces, vuelve a Ajustes → *Menú secreto*
   → activa *Usar almacén de CA de terceros*.

### Windows 10 / 11

1. Haz doble clic en `root.crt` → **Instalar certificado…**
2. Ubicación: **Equipo local** (pide administrador) → **Colocar todos los certificados en el
   siguiente almacén** → **Entidades de certificación raíz de confianza** → Finalizar.
3. Chrome y Edge lo usan al instante. Firefox: **Ajustes → Privacidad y seguridad →
   Certificados → Ver certificados → Autoridades → Importar**, marca *Confiar para
   identificar sitios web*.

### macOS

1. Doble clic en `root.crt` → se abre **Acceso a Llaveros** y lo añade al llavero *inicio de sesión*.
2. Búscalo (**Caddy Local Authority**), doble clic → **Confiar → Al usar este certificado:
   Confiar siempre**. Pide tu contraseña.
3. Safari y Chrome lo usan. Firefox: igual que en Windows (importar en Autoridades).

O por terminal:

```bash
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain root.crt
```

### Linux (Debian/Ubuntu)

```bash
sudo cp root.crt /usr/local/share/ca-certificates/guardian-root.crt
sudo update-ca-certificates
```

Chrome/Chromium en Linux usan su propio almacén (NSS): `certutil -d sql:$HOME/.pki/nssdb -A
-t "C,," -n guardian-root -i root.crt` (paquete `libnss3-tools`).

Comprueba: abre `https://id.ai.home` y debe aparecer el candado sin avisos.

## 5. Primer login: Pocket ID

Pocket ID es tu proveedor de identidad. Todo el que quiera usar el chat pasa por él.

1. Abre `https://id.ai.home/setup` (solo funciona la primera vez).
2. Rellena **Username**, **Email**, nombre y apellido y pulsa **Sign Up**.
3. Pulsa **Add Passkey** y sigue el diálogo del navegador (Face ID, huella, PIN de Windows
   Hello o tu llave física). Esa passkey es tu forma de entrar: **registra una segunda**
   en cuanto puedas (por ejemplo, la del móvil y la del portátil).
4. Crea el cliente OIDC para Open WebUI: **Administration → OIDC Clients → Add OIDC Client**
   - **Name**: `open-webui`
   - **Client Launch URL**: `https://ai.home/`
   - **Callback URLs** → **Add**: `https://ai.home/oauth/oidc/callback`
   - **Save**
5. Pestaña **Allowed User Groups**: en Pocket ID 2.14 el cliente nace *restringido* sin grupos
   y nadie podría entrar. Pulsa **Unrestrict** y confirma (o crea un grupo con tus usuarios y
   asígnalo). Si lo olvidas, verás "You are not allowed to access this service".
6. Pestaña **Credentials → Add client secret**. Copia el secreto: solo se muestra una vez.
7. Copia el **Client ID** (arriba en la ficha del cliente) y el secreto a `compose/.env`:

   ```
   OAUTH_CLIENT_ID=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
   OAUTH_CLIENT_SECRET=el-secreto-que-copiaste
   ```

8. Reinicia Open WebUI para que lea el `.env`:

   ```bash
   sudo make restart
   ```

9. Abre `https://ai.home`. Verás únicamente el botón **Continue with Pocket ID** (el formulario
   de usuario y contraseña está desactivado). Entra con tu passkey y acepta el consentimiento.
   **El primer usuario que entra se convierte en administrador** de Open WebUI.

Para invitar a más personas: en Pocket ID, **Administration → Users → Add User**; la persona
recibe un enlace de un solo uso para registrar su passkey. La primera vez que entre en el chat,
Open WebUI le creará su cuenta (con el rol por defecto, *user*).

### Descargar un modelo

Open WebUI → icono de usuario → **Admin Panel → Settings → Models** → escribe el nombre
(por ejemplo `llama3.1:8b`) y pulsa descargar. Ollama lo guarda en el volumen `ollama_data`.

## 6. Comprobar que todo está bien

En el host:

```bash
sudo make doctor
```

Debe mostrar `[ OK ]` en las 13 comprobaciones: Docker, los 13 servicios, ningún socket del host
en 11434, solo 443/tcp y 51820/udp publicados, la CA exportada, Ollama accesible desde el
contenedor de Open WebUI, el certificado de 443 emitido por la CA interna, la red de agentes
interna, una sonda en esa red sin salida a Internet, ningún agente con el socket de Docker, el
socket montado solo en el proxy de solo lectura, Loki ingiriendo y el destino ntfy configurado.

Desde **otro** equipo de tu LAN (necesita `nmap`):

```bash
nmap -sT -p- 10.20.0.10          # solo 22/tcp y 443/tcp
nmap -sU -p 51820 10.20.0.10     # 51820/udp open|filtered
curl -m 3 http://10.20.0.10:11434  # debe fallar (Ollama no es alcanzable)
```

## 7. Acceso remoto con WireGuard

WireGuard es una VPN: tu móvil "está en casa" aunque estés fuera. Es lo **único** que se
abre hacia Internet.

### En el router

Redirige el puerto **51820/udp** de tu IP pública a la IP del host (`10.20.0.10`). Nada más.
Si tu operador te da IP dinámica, usa un servicio de DNS dinámico (DuckDNS, Cloudflare…) y
pon ese nombre en `WG_HOST`.

### Asistente inicial de wg-easy (una sola vez)

1. Abre `https://vpn.ai.home`. wg-easy 15 muestra su asistente.
2. **Continue** → crea el **usuario y contraseña de administración** del panel (esto no es
   la VPN; es solo para entrar al panel). Usa un gestor de contraseñas.
3. "Do you have an existing setup?" → **No**.
4. **Host**: tu `WG_HOST` (nombre DNS público o IP pública). **Port**: `51820`. **Continue**.
5. **Setup successful → Sign In**.

### Añadir tu móvil

1. Instala la app **WireGuard** (App Store / Google Play).
2. En el panel: **New Client** → **Name** (`movil-ana`; la fecha de caducidad es opcional) →
   **Create Client**. El cliente aparece con su IP (`10.8.0.2`, `10.8.0.3`, …).
3. En la fila del cliente hay tres iconos: **QR**, **descargar** (`.conf`) y **editar**.
   Pulsa el **QR**; en el móvil, WireGuard → **+** → **Crear a partir de código QR**, ponle
   nombre y guarda.
4. Activa el túnel (con datos móviles, no con tu Wi-Fi). Abre `https://ai.home`: debe cargar,
   pedir login en Pocket ID y el chat funcionar.
5. Para un portátil, descarga el `.conf` e impórtalo en la app de escritorio de WireGuard.

Notas:

- La red de la VPN es `10.8.0.0/24` (la primera regla del firewall ya la permite).
- Para que `ai.home` se resuelva a través de la VPN, en el panel de wg-easy pon como **DNS**
  de los clientes la IP de tu DNS local (Pi-hole, AdGuard, router). Si no tienes DNS local,
  puedes crear una entrada en la app del móvil… pero es más cómodo tener DNS de red.
- Comprueba desde fuera (datos móviles, sin Wi-Fi) que **solo** responde 51820/udp.

## 8. Agentes y API para aplicaciones

Con la base funcionando, `https://api.<DOMAIN>/v1` ofrece una API compatible con OpenAI con
llaves por aplicación, y `guardianctl agent run` ejecuta agentes aislados. Está explicado en
[`docs/agentes.md`](agentes.md).

## 9. Auditoría y alertas

Los logs de todo (logins, llamadas al modelo, egreso de agentes, firewall) se consultan en
`https://logs.<DOMAIN>` y las alertas llegan al móvil por ntfy. Está explicado en
[`docs/auditoria.md`](auditoria.md).

## 10. Operación diaria

```bash
sudo make ps        # estado
sudo make logs      # logs en vivo
sudo make down      # parar todo (los datos quedan en los volúmenes)
sudo make up        # arrancar
sudo make doctor    # comprobaciones
```

Actualizar imágenes (en desarrollo se usan tags; antes de un release irán por digest):

```bash
cd guardian && git pull
sudo docker compose --env-file compose/.env -f compose/docker-compose.yml pull
sudo make up
```

Copias de seguridad: `compose/.env` (secretos), `compose/certs/root.crt` y los volúmenes
Docker `guardian_pocket_id_data`, `guardian_open_webui_data`, `guardian_wg_easy_data`,
`guardian_caddy_data` (contiene la clave de la CA). En v0.2 llegará `restic` integrado.

## Problemas frecuentes

**El navegador avisa de certificado no seguro.** No has instalado `root.crt` en ese
dispositivo (paso 4) o, en iOS, falta activar la confianza total. En Firefox hay que
importarla aparte.

**"You are not allowed to access this service" al entrar en el chat.** El cliente OIDC está
restringido a grupos sin miembros. Pocket ID → cliente `open-webui` → *Allowed User Groups*
→ **Unrestrict**.

**En `https://ai.home` no aparece el botón de Pocket ID.** `OAUTH_CLIENT_ID` o
`OAUTH_CLIENT_SECRET` vacíos en `compose/.env`, o no has hecho `sudo make restart` después de
editarlo. Mira `sudo docker logs gd-open-webui | grep -i oauth`.

**Quiero volver a tener el formulario de contraseña (por ejemplo, perdí la passkey).**
`ENABLE_LOGIN_FORM` es configuración persistente de Open WebUI: se lee del entorno solo en el
primer arranque. Edita `compose/.env` con `ENABLE_LOGIN_FORM=true` y `ENABLE_PERSISTENT_CONFIG=false`,
ejecuta `sudo make restart`, entra, y vuelve a dejar `ENABLE_PERSISTENT_CONFIG=true`.
Mejor prevención: registra dos passkeys en Pocket ID.

**Open WebUI tarda mucho en estar "healthy" la primera vez.** Al arrancar descarga el modelo de
embeddings (`all-MiniLM-L6-v2`, ~90 MB) de Hugging Face. Con conexión lenta pueden ser varios
minutos. Se guarda en el volumen y no se repite.

**Apliqué nftables y perdí el acceso.** La regla de rescate permite SSH desde `LAN_NET` y
`AI_NET`. Si tu red no es `10.10.10.0/24` / `10.20.0.0/24`, entra por consola/teclado y ejecuta
`sudo nft delete table inet guardian`, corrige los `define` y vuelve a aplicar.

**Los contenedores se quedaron sin Internet después de tocar nftables.** Si ejecutaste
`nft flush ruleset` a mano, has borrado las tablas de Docker: `sudo systemctl restart docker`
y luego `make nft-apply`.

**`make doctor` falla en "Ollama responde desde gd_ai".** Ollama aún está arrancando o
`open-webui` no está en la red `gd_ai`. `sudo docker logs gd-ollama`.

**Pocket ID se reinicia solo de vez en cuando** (`docker logs gd-pocket-id` muestra "Health check
failed … host is not registered"). Lo observamos en las VMs de prueba en ambos sistemas, con y sin
`TZ`, y los reinicios coincidían en el mismo minuto en las dos VMs del mismo anfitrión, lo que
apunta a saltos de reloj de la máquina virtual y no a la configuración. El contenedor vuelve en
un segundo (`restart: unless-stopped`) y las sesiones se conservan. Si te ocurre en hardware
real, revisa la sincronización horaria (`timedatectl`) y abre una incidencia con los logs.

**Uso una GPU.** Descomenta el bloque NVIDIA o AMD/ROCm del servicio `ollama` en
`compose/docker-compose.yml` (NVIDIA requiere `nvidia-container-toolkit` en el host).
