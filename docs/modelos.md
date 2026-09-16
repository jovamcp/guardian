# Modelos de Ollama: integridad y fijación

Un modelo manipulado (en el registro, en tránsito o en el disco) es una de las amenazas del
modelo de DESIGN.md §3. Desde v0.4 `guardianctl model` cubre dos cosas:

- **Integridad**: cada modelo de Ollama es un manifiesto JSON que enumera *blobs* por SHA-256.
  `model verify` recalcula el SHA-256 de cada blob y lo compara con su nombre. Un byte cambiado
  en el archivo GGUF se detecta.
- **Fijación**: `model pin` guarda en `guardian.yaml` (`models:`) el digest del manifiesto de cada
  modelo. Si alguien vuelve a descargar `llama3.1:8b` y el registro sirve otro contenido, el
  digest cambia y `doctor` falla.

Todo es de solo lectura sobre el volumen `ollama_data`; ni Ollama ni los modelos se tocan.

## Uso

```bash
sudo ./bin/guardianctl model list            # modelos, digest corto, tamaño y estado (fijado / sin fijar / CAMBIADO)
sudo ./bin/guardianctl model pin --all       # fija todos los modelos actuales en guardian.yaml
sudo ./bin/guardianctl model pin qwen2.5:0.5b
sudo ./bin/guardianctl model unpin qwen2.5:0.5b
sudo ./bin/guardianctl model verify          # SHA-256 de todos los blobs (lee GB: tarda)
sudo ./bin/guardianctl model verify --report qwen2.5:0.5b   # solo uno, y envía el resultado a Vector
```

`doctor` (y su timer de 15 minutos) hace la comprobación barata: manifiestos fijados sin cambios
y blobs presentes con su tamaño. Falla (rojo) si un modelo fijado cambió de digest o le falta un
blob; avisa (amarillo) si hay modelos sin fijar o fijados que ya no están en Ollama.

## Auditoría y alerta

Cada comprobación emite un evento `model_check` a Vector (`mode` doctor|verify, `status`
ok|warn|fail, `problems[]` con modelo, tipo y detalle). El panel **Guardian · Estado** muestra el
último resultado y la alerta *Modelo de Ollama alterado o incompleto* avisa por ntfy si hay un
`fail` en la última hora.

Tipos de problema: `digest_changed` (re-pull con otro contenido), `blob_missing`, `blob_size`
(truncado), `blob_mismatch` (SHA-256 distinto: alterado), `unpinned`, `pinned_missing`.

## Flujo recomendado

1. Tras descargar o actualizar un modelo a propósito: `model verify <modelo>` y `model pin <modelo>`.
2. `doctor` en verde a partir de ahí; si se pone en rojo sin que hayas tocado nada, no uses el
   modelo hasta aclararlo (`model verify` dice qué blob no cuadra).
3. `model verify` completo de vez en cuando (por ejemplo, semanal, con un timer o a mano): es lo
   único que detecta un blob alterado en disco con el mismo tamaño.

## Runtime fuera de Docker

Si Ollama corre fuera del compose (`runtime.kind` distinto o instalación previa), indica el
directorio: `guardianctl model list --store /usr/share/ollama/.ollama/models`. `doctor` solo
comprueba el volumen del compose.
