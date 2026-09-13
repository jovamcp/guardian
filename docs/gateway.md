# El gateway LLM de Guardian (gd-gateway)

`gd-gateway` es el punto único por el que apps y agentes hablan con los modelos. Es un proxy
compatible con la API de OpenAI escrito en Go (solo biblioteca estándar, ~3 MB, ~5 MB de RAM)
que sustituye desde v0.3 a LiteLLM y Postgres. No traduce formatos: Ollama ya expone `/v1/*`
compatible con OpenAI. Lo que añade es lo que Guardian necesita:

| Función | Cómo |
|---|---|
| Llaves virtuales | `guardianctl key create` → `sk-gd-…`; se guarda solo el hash |
| Modelos por llave | lista o `*`; otro modelo → `403 key_model_access_denied` |
| Límite de peticiones | `--rpm` por llave (ventana deslizante) → `429` |
| Multi-nodo | varios upstreams por modelo con salud, reparto y failover |
| Cloud burst controlado | upstreams `cloud: true`, permiso por llave (`--cloud`), presupuestos → `402` |
| Auditoría | un evento por petición hacia Vector/Loki (solo metadatos) |
| Sin base de datos | `keys.json` y `usage.json` en el volumen `gateway_data` |

Rutas: `https://api.<DOMAIN>/v1` desde la LAN/VPN y `http://gateway:4000/v1` desde los agentes.
Soporta `/v1/models`, `/v1/chat/completions` (con `stream`), `/v1/completions` y `/v1/embeddings`.

## Llaves

```bash
sudo ./bin/guardianctl key create --name n8n --models qwen2.5:0.5b,llama3.1:8b --rpm 60
sudo ./bin/guardianctl key create --name analista --models "*" --cloud --budget 5   # USD/mes en cloud
sudo ./bin/guardianctl key list        # alias, prefijo, cloud, gasto, peticiones, modelos
sudo ./bin/guardianctl key delete n8n  # por alias, id o prefijo
```

La master key (`GATEWAY_MASTER_KEY` en `compose/.env`) solo sirve para administrar; el gateway
la rechaza en `/v1/*`. Prueba desde cualquier equipo con la CA instalada:

```bash
curl https://api.ai.home/v1/chat/completions -H "Authorization: Bearer sk-gd-…" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen2.5:0.5b","messages":[{"role":"user","content":"hola"}]}'
```

## Nodos (multi-nodo)

`compose/gateway/config.yaml`:

```yaml
upstreams:
  - name: ollama            # el Ollama de este host
    type: ollama
    url: http://ollama:11434
    models: ["*"]           # descubre los modelos instalados
  - name: node2             # otro mini-PC con Ollama en la zona de IA (o por WireGuard)
    type: ollama
    url: http://10.20.0.11:11434
    models: ["*"]
    weight: 2               # recibe el doble de peticiones
```

Tras editar: `sudo make restart`. El gateway comprueba cada nodo cada 30 s (`/api/tags`), reparte
las peticiones entre los sanos que sirvan el modelo y, si uno falla a mitad, reintenta en el
siguiente. `sudo make doctor` avisa de los nodos caídos; `X-Guardian-Upstream` en la respuesta
dice quién contestó.

En el segundo nodo, Ollama debe escuchar en su IP (`OLLAMA_HOST=0.0.0.0`) y su firewall permitir
el 11434 **solo** desde la IP del host Guardian. Sigue sin publicarse ningún puerto en Guardian.

## Cloud burst controlado

Cualquier API compatible con OpenAI (OpenAI, OpenRouter, Groq, Mistral…):

```yaml
upstreams:
  - name: openai
    type: openai
    url: https://api.openai.com
    api_key_env: GATEWAY_UPSTREAM_OPENAI_KEY   # el valor va en compose/.env
    cloud: true
    models: [gpt-4o-mini]
    prices:                                    # USD por millón de tokens
      gpt-4o-mini: {input: 0.15, output: 0.60}
budget:
  cloud_monthly_usd: 10                        # tope global; 0 = sin límite
```

Reglas: solo las llaves creadas con `--cloud` ven y usan modelos cloud; cada respuesta se
valora con la tabla de precios; al 80 % del presupuesto global se emite un evento `budget`
(alerta en Grafana → ntfy) y al 100 % (global o de la llave) el gateway responde `402`. Los
modelos locales siguen funcionando aunque el presupuesto cloud esté agotado. El gasto se
reinicia cada mes (`/admin/usage`, `key list`).

Un modelo es "cloud" solo si **ningún** nodo local lo sirve: si declaras el mismo nombre en un
nodo Ollama, se preferirá siempre el local.

## Auditoría y privacidad

Cada petición genera un evento con alias de llave, modelo, nodo, `cloud`, estado HTTP, tokens,
coste y latencia. **Nunca** el prompt ni la respuesta. Se ven en el panel *Guardian · LLM*.

## Migración desde v0.2 (LiteLLM)

1. `git pull` (o el release) y `sudo ./install.sh`: construye la imagen del gateway, genera
   `GATEWAY_MASTER_KEY` y retira `litellm` y `litellm-db`.
2. Vuelve a emitir las llaves con `guardianctl key create` (las de LiteLLM estaban hasheadas en
   Postgres y no se pueden migrar) y actualízalas en tus apps y manifiestos (`llm.key: auto`
   no requiere nada).
3. Copia los modelos que tenías en `compose/litellm/config.yaml` como nodos/`models` del
   gateway si no usas `"*"`.
4. Cuando todo funcione: `sudo docker volume rm guardian_litellm_db_data`.

## API de administración

Con `Authorization: Bearer <GATEWAY_MASTER_KEY>` en `https://api.<DOMAIN>`:
`POST/GET /admin/keys`, `DELETE /admin/keys/{id|alias|prefijo}`, `GET /admin/health` (nodos y
modelos), `GET /admin/usage` (gasto del mes). `GET /health` responde 200 si hay algún nodo sano.
