# Programa beta de Guardian (v0.4)

Buscamos **cinco personas** con un homelab que ya ejecute Ollama y quieran probar Guardian
durante dos semanas. El objetivo de la beta es medir una sola cosa: **¿se instala en menos de
15 minutos y termina con `doctor` en verde?** Todo lo demás (bugs, dudas, ideas) también nos
interesa, pero eso es lo primero.

## Perfil

- Mini-PC o servidor con **Debian 12** o **Ubuntu 24.04 Server** limpio (sin Docker previo), 8 GB
  de RAM, IP fija.
- Un router o firewall en el que puedas crear una VLAN y reglas (FortiGate, OPNsense, UniFi,
  MikroTik…). Si no puedes, Guardian funciona igual dentro de tu LAN; solo pierdes la
  separación de zona.
- Un móvil para las passkeys y para WireGuard.
- Ganas de anotar tiempos y pegar salidas de comandos.

## Antes de empezar

1. Descarga el release desde <https://github.com/jovamcp/guardian/releases>: el tarball
   `guardian-<versión>.tar.gz` y `SHA256SUMS`. Comprueba la suma:

   ```bash
   sha256sum -c --ignore-missing SHA256SUMS
   tar -xzf guardian-<versión>.tar.gz && cd guardian-<versión>
   ```

   Los releases van firmados con **cosign** (keyless, identidad del workflow de GitHub).
   Si tienes cosign, comprueba también la firma de `SHA256SUMS`:

   ```bash
   cosign verify-blob --certificate SHA256SUMS.pem --signature SHA256SUMS.sig \
     --certificate-identity-regexp '^https://github.com/jovamcp/guardian/.github/workflows/release.yml@' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
   ```

   Descomprime en `/opt` como root si vas a usar timers (copias, agentes programados, panel de
   estado): el código que ejecutan como root debe pertenecer a root.
   (Alternativa: `sudo git clone https://github.com/jovamcp/guardian.git /opt/guardian`; entonces
   necesitas Go ≥ 1.22 o dejar que `install.sh` descargue el binario del release.)

2. Ten a mano: tu dominio local (`ai.home` vale), la red de tu LAN, la red y VLAN que darás a la
   zona de IA, la IP del host y tu nombre DNS público o IP para WireGuard.

## Cronómetro

Arranca el cronómetro al ejecutar `sudo ./install.sh` y páralo cuando `sudo make doctor` muestre
todo en verde. Apunta también cuánto tardaste en los pasos manuales (DNS local, CA en el móvil,
cliente OIDC en Pocket ID, WireGuard). Los pasos están en `docs/instalacion.md`.

## Checklist de la beta

Marca lo que consigas y anota el tiempo:

- [ ] `sudo ./install.sh` termina sin errores (____ min).
- [ ] `guardianctl init` te preguntó lo justo y `guardian.yaml` refleja tu red.
- [ ] DNS local creado para `<DOMAIN>`, `id.`, `api.`, `logs.`, `vpn.` (____ min).
- [ ] CA instalada en un móvil y un ordenador; `https://id.<DOMAIN>` sin avisos (____ min).
- [ ] Admin de Pocket ID con passkey y cliente `open-webui` creado; login en el chat (____ min).
- [ ] Modelo descargado y respuesta en el chat.
- [ ] `sudo make doctor` en verde (hora de parar el cronómetro: ____ min en total).
- [ ] Desde otro equipo de la LAN: `nmap -sT -p- <ip-host>` solo 22 y 443.
- [ ] WireGuard desde datos móviles: el chat carga.
- [ ] Política del firewall aplicada (`policy render fortios|opnsense`) y AI→LAN bloqueado.
- [ ] `hello-agent` ejecutado (`docs/agentes.md`) y su alerta llegó a ntfy (`docs/auditoria.md`).
- [ ] `sudo ./bin/guardianctl model pin --all` y `doctor` sigue en verde (`docs/modelos.md`).
- [ ] Un agente real (`agent run hermes-agent` u `openclaw`) responde y no sale a Internet.
- [ ] Cuando salga la siguiente versión: `sudo ./bin/guardianctl upgrade` termina con `doctor` en verde (____ min) y `--rollback` funciona (`docs/actualizacion.md`).

## Qué reportar

Abre un issue por cada problema con la plantilla **Beta**. Incluye siempre:

- Distribución, CPU (x86-64 o ARM), GPU si la hay, y dónde corre (metal, Proxmox, VM).
- Salida de `sudo make doctor` y de `sudo docker compose --env-file compose/.env -f compose/docker-compose.yml ps`.
- El paso de `docs/instalacion.md` donde te atascaste y qué esperabas que pasara.
- Tiempos del cronómetro, aunque no hayas terminado.

**No pegues nunca** `compose/.env`, `vault/`, llaves `sk-…` ni secretos de clientes OIDC.
`root.crt` sí se puede compartir (es solo la parte pública).

## Resultados en hardware real

| Fecha | Máquina | SO | Versión | Instalación | `doctor` | Notas |
|---|---|---|---|---|---|---|
| 2026-09 | Apple Silicon (VMs Lima Debian 12 / Ubuntu 24.04, arm64) | Debian 12, Ubuntu 24.04 | v0.1–v0.3 | < 15 min | verde | Entorno de desarrollo; ver trampas en `CLAUDE.md` |
| 2026-09-16 | Apple Silicon (VM Lima Debian 12, arm64) | Debian 12 | v0.3.0-dev → v0.4.0-rc1 → rc2 (`guardianctl upgrade`), `--rollback` a rc1 y vuelta | ~2 min por salto (copia restic de 1,1 GB incluida) | verde salvo timers (repo no es de root en la VM) | Migración LiteLLM, `nftables/guardian.nft.new`, `model pin`, Hermes Agent y OpenClaw ejecutados en el sandbox con auditoría en Loki. Lección: las imágenes de agentes (8,4 GB) llevaron el disco al 95 % y Loki dejó de ingerir → comprobación de disco en `doctor` |
| pendiente | x86-64 (EC2 o mini-PC) | Debian 12 | v0.4.0-rc | — | — | Primera prueba fuera de Apple Silicon (tarea 6 de `docs/prompts/07-v0.4.md`) |

## Qué NO está todavía

Agentes permanentes (`mode: service`: gateway de OpenClaw o Hermes con canales de mensajería),
autenticación de proxy por agente, firma de eventos de auditoría, dashboard propio, Tailscale como
alternativa a wg-easy, Podman rootless. Si lo necesitas, dilo en un issue con la etiqueta `v0.5`.
