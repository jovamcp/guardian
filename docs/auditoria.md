# Auditoría y alertas (Fase 3)

Principio 4 de Guardian: **todo flujo deja rastro**. Esta capa recoge los logs de todos los
servicios y del firewall, los guarda `LOKI_RETENTION_PERIOD` (30 días por defecto), los muestra en
Grafana y avisa al móvil cuando pasa algo que debes mirar.

## Qué se registra y de dónde sale

| Evento (`event`) | Servicio | Qué significa |
|---|---|---|
| `login_authorize`, `login_token`, `auth_error` | Pocket ID | Inicio de sesión OIDC (autorización y canje del código) y errores de autenticación |
| `http_access` | Caddy | Cada petición HTTPS a `<DOMAIN>`, `id.`, `api.`, `vpn.`, `logs.` (host, ruta, estado, IP) |
| `llm_request`, `budget` | gd-gateway | Cada llamada al gateway: alias de la llave, modelo, nodo, `cloud`, tokens, coste, estado; aviso de presupuesto. **Nunca prompts ni respuestas** |
| `egress_allowed`, `egress_denied` | Squid | Decisión del proxy para cada `CONNECT` de un agente (IP del agente y destino) |
| `dns_blocked`, `dns_resolved` | Blocky | Consultas DNS de los agentes y si se bloquearon |
| `fw_drop_input`, `fw_drop_forward` | nftables (kernel) | Paquetes descartados hacia el host o hacia puertos publicados |
| `model_check` | guardianctl | Resultado de la comprobación de modelos de Ollama (`doctor`: digests fijados y blobs presentes; `model verify`: SHA-256 de cada blob). `status` ok/warn/fail y lista `problems` |
| `log` | todos | Cualquier otra línea de log de un contenedor `gd-*` |

**Disco**: Loki deja de ingerir cuando el sistema de archivos de Docker supera el 90 % (su WAL entra en
throttling y responde `Ingester is shutting down`); Vector reintenta y no se pierde nada si liberas
espacio pronto. `doctor` avisa al 80 % y falla al 90 %.

Cómo llega a Loki:

1. **docker-socket-proxy** es el único contenedor que ve `/var/run/docker.sock` (montado de solo
   lectura) y solo deja pasar `GET` de contenedores, logs y eventos. Vive en la red interna `gd_audit`.
2. **Vector** lee los logs de los contenedores a través de ese proxy, el journal del kernel
   (montaje de solo lectura) y un webhook interno al que gd-gateway envía un evento por petición.
   Normaliza cada línea (`service`, `event`, `client_ip`, `destination`, …) y la envía a **Loki**.
3. **Loki** guarda en disco (`loki_data`) con retención por compactor.
4. **Grafana** (`https://logs.<DOMAIN>`) consulta Loki. Login solo con **Pocket ID**; los miembros
   del grupo `admins` de Pocket ID entran como administradores, el resto como lectores.

Nada de esto publica puertos nuevos: sigue habiendo solo 443/tcp y 51820/udp.

## Ponerlo en marcha

1. Crea el cliente OIDC `grafana` en Pocket ID (igual que el de Open WebUI, ver
   `docs/instalacion.md`), con callback `https://logs.<DOMAIN>/login/generic_oauth`, pulsa
   **Unrestrict** en *Allowed User Groups* y crea un secreto en *Credentials*.
2. Opcional: crea el grupo `admins` en Pocket ID (**Administration → User Groups → Add Group**,
   *Name* `admins`) y añádete. Sin grupo entras como lector.
3. En `compose/.env`:

   ```
   GRAFANA_OAUTH_CLIENT_ID=…
   GRAFANA_OAUTH_CLIENT_SECRET=…
   NTFY_URL=https://ntfy.sh          # o tu servidor ntfy
   NTFY_TOPIC=un-nombre-dificil-de-adivinar
   LOKI_RETENTION_PERIOD=720h        # 30 días
   ```

4. `sudo make restart` y entra en `https://logs.<DOMAIN>`. Los tres paneles de la carpeta
   **Guardian** se provisionan solos: *Accesos*, *LLM* y *Egreso de agentes*.
5. Instala la app **ntfy** en el móvil y suscríbete al topic. Si usas `ntfy.sh` público, el topic
   es la única "contraseña": elige uno largo y aleatorio, o levanta tu propio ntfy.

## Alertas incluidas

| Alerta | Cuándo | Gravedad |
|---|---|---|
| Egreso denegado a un agente | Squid deniega un `CONNECT` o Blocky bloquea un dominio en los últimos 5 min | warning |
| Fallos de autenticación repetidos | 5 o más errores de Pocket ID en 10 min | warning |
| Tráfico descartado hacia servicios publicados | descartes `gd-drop-fwd` en 15 min (alguien fuera de LAN/VPN probando el 443) | info |

Se evalúan cada minuto y llegan a ntfy con la plantilla nativa de Grafana (título, valor y
etiquetas). Las reglas viven en `compose/grafana/provisioning/alerting/guardian.yaml`: añade las
tuyas ahí, no en la interfaz (Grafana está provisionado y no permite editarlas).

## Agentes programados

`schedule.cron` del manifiesto se convierte en un **timer de systemd** del host, que ejecuta
`sandbox/runner.sh <agente>` (es decir, `guardianctl agent run`) con el sandbox completo:

```bash
sudo ./bin/guardianctl agent schedule show mi-agente    # muestra las unidades que generaría
sudo ./bin/guardianctl agent schedule apply             # escribe y habilita los timers de todos
sudo ./bin/guardianctl agent schedule list
sudo journalctl -u guardian-agent-mi-agente.service     # salida de cada ejecución
sudo ./bin/guardianctl agent schedule remove mi-agente
```

Cron de 5 campos (`*/15 * * * *`, `0 3 * * 1-5`, …). Cada ejecución crea y destruye su llave
LLM efímera y sus secretos, como una ejecución manual.

## Privacidad y límites

- gd-gateway envía a Vector solo metadatos; ni prompts ni respuestas salen del gateway. Vector
  además filtra los campos a una lista blanca.
- Los logs de Caddy incluyen IP de origen, host y ruta; no cuerpos ni cabeceras.
- Loki no tiene autenticación propia: solo es alcanzable desde `gd_audit` (Vector, Grafana) y por
  la sonda de `doctor`. No lo expongas.
- Vector ve el contenido de los logs de todos los contenedores; por eso no tiene el socket de
  Docker ni salida a Internet (`gd_audit` es interna).
- Si Loki arranca a la vez que Vector puede responder 500 unos segundos ("at least 1 live
  replicas"); Vector reintenta y no se pierde nada.

## Panel de estado (v0.2)

`sudo ./bin/guardianctl doctor schedule apply` crea un timer que ejecuta `doctor --report` cada
15 minutos; el informe (JSON con cada comprobación) llega a Vector y al panel **Guardian · Estado**:
comprobaciones OK, avisos y fallos del último informe, la tabla de problemas y el historial.
`doctor --json` imprime el mismo informe por consola.

## Copias de seguridad (v0.2)

`docs/instalacion.md` § "Copias de seguridad" explica `guardianctl backup`. La última copia se
refleja en `doctor` (aviso si tiene más de 48 h) y, por tanto, en el panel de estado.
