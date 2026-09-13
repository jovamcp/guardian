# Programa beta de Guardian v0.1

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

   Desde v0.2 los releases van firmados con **cosign** (keyless, identidad del workflow de GitHub).
   Si tienes cosign, comprueba también la firma de `SHA256SUMS`:

   ```bash
   cosign verify-blob --certificate SHA256SUMS.pem --signature SHA256SUMS.sig \
     --certificate-identity-regexp '^https://github.com/jovamcp/guardian/.github/workflows/release.yml@' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
   ```

   (Alternativa: `git clone https://github.com/jovamcp/guardian.git`; entonces necesitas Go ≥ 1.22
   o dejar que `install.sh` descargue el binario del release.)

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

## Qué reportar

Abre un issue por cada problema con la plantilla **Beta**. Incluye siempre:

- Distribución, CPU (x86-64 o ARM), GPU si la hay, y dónde corre (metal, Proxmox, VM).
- Salida de `sudo make doctor` y de `sudo docker compose --env-file compose/.env -f compose/docker-compose.yml ps`.
- El paso de `docs/instalacion.md` donde te atascaste y qué esperabas que pasara.
- Tiempos del cronómetro, aunque no hayas terminado.

**No pegues nunca** `compose/.env`, `vault/`, llaves `sk-…` ni secretos de clientes OIDC.
`root.crt` sí se puede compartir (es solo la parte pública).

## Qué NO está en v0.1

Multi-nodo, cloud burst, dashboard propio, gVisor, firma de releases con cosign, plantilla
UniFi, copias con restic, dominio público con DNS challenge. Si lo necesitas, dilo en un issue
con la etiqueta `v0.2`.
