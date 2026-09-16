# Guardian en un Mac con chip Apple (M1–M4)

Guardian es software para Linux (Docker rootful, nftables, AppArmor). En un Mac con Apple
Silicon corre **dentro de una máquina virtual Linux arm64**, que es exactamente el entorno en el
que se ha desarrollado y probado v0.4 (Lima 2.2, Apple Virtualization, Debian 12, Mac M-series).
Lo mismo aplica a cualquier ARM64 con Debian 12 o Ubuntu 24.04 (Raspberry Pi 5, mini-PC ARM):
los binarios y todas las imágenes son multi-arch.

## Qué necesitas

- macOS 14 o superior en un Mac M1–M4, 16 GB de RAM (8 GB para la VM) y **60 GB libres** de
  disco para la VM si vas a ejecutar agentes (las imágenes de Hermes Agent y OpenClaw suman
  8,4 GB; Loki deja de ingerir logs si el disco de la VM pasa del 90 %).
- [Homebrew](https://brew.sh) y Lima: `brew install lima`.
- Decidir cómo quieres llegar a Guardian (ver *Red* abajo): solo desde el Mac, o desde toda la
  LAN y desde fuera por WireGuard.

## 1. Crear la VM

```bash
limactl create --name=guardian --vm-type=vz --cpus=4 --memory=8 --disk=60 \
  --network=vzNAT template://debian-12
limactl start guardian
limactl shell guardian
```

`--network=vzNAT` da a la VM una IP fija (`192.168.64.x`) alcanzable **solo desde el Mac**.
Es suficiente para probar y para usar el chat desde el propio Mac. Para la LAN, ver *Red*.

## 2. Instalar Guardian dentro de la VM

Igual que en cualquier Debian, con dos detalles propios de la VM:

```bash
# Como root en /opt: los timers (copias, doctor, agentes programados) exigen que el repo sea de root.
curl -fsSLO https://github.com/jovamcp/guardian/releases/download/v0.4.0/guardian-0.4.0.tar.gz
curl -fsSLO https://github.com/jovamcp/guardian/releases/download/v0.4.0/SHA256SUMS
sha256sum -c --ignore-missing SHA256SUMS
sudo tar -xzf guardian-0.4.0.tar.gz -C /opt && sudo mv /opt/guardian-0.4.0 /opt/guardian
cd /opt/guardian && sudo ./install.sh
```

Cuando `guardianctl init` pregunte el dominio, sin DNS propio lo más cómodo es
`<ip-de-la-vm-con-guiones>.sslip.io` (por ejemplo `192-168-64-7.sslip.io`): resuelve solo a
la IP de la VM y vale para `id.`, `api.`, `logs.` y `vpn.`.

**Apple Virtualization y OpenSSL.** Open WebUI muere con `SIGILL` en estas VMs (la biblioteca
`cryptography` detecta instrucciones SVE2 que la VM no ofrece). `install.sh` lo detecta
(`systemd-detect-virt` = `apple`) y crea `compose/local.yml` con `OPENSSL_armcap: "0"` para ese
servicio. Si instalas de otra forma, crea tú ese archivo antes de `make up`:

```yaml
services:
  open-webui:
    environment:
      OPENSSL_armcap: "0"
```

`compose/local.yml` está ignorado por git y `guardianctl upgrade` no lo toca.

## 3. Usarlo desde el Mac

1. Copia la CA: `limactl copy guardian:/opt/guardian/compose/certs/root.crt ~/Downloads/` y
   añádela al llavero: `sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ~/Downloads/root.crt`.
2. Abre `https://id.<DOMAIN>/setup`, crea el administrador con Touch ID y el cliente OIDC
   `open-webui` como indica `docs/instalacion.md`.
3. Chat en `https://<DOMAIN>`, paneles en `https://logs.<DOMAIN>`.

## Red: solo Mac, LAN o Internet

| Quieres | Red de Lima | Notas |
|---|---|---|
| Probar y usar desde el Mac | `--network=vzNAT` (arriba) | Nadie más en casa llega a la VM. WireGuard no sirve. |
| Que toda la LAN lo use | `socket_vmnet` en modo *bridged* | `brew install socket_vmnet`, `limactl sudoers \| sudo tee /etc/sudoers.d/lima`, y en el YAML de la instancia `networks: [{lima: bridged}]` con tu interfaz (`en0`). La VM recibe IP de tu router como un equipo más; ponle reserva DHCP. |
| Entrar desde fuera por WireGuard | *bridged* + redirección de `51820/udp` en el router hacia la IP de la VM | El Mac debe estar encendido y sin dormir (`caffeinate` o Ajustes → Batería). |

Guía de Lima: <https://lima-vm.io/docs/config/network/>.

## Lo que sabemos de este entorno

- Probado en v0.4.0: instalación, `upgrade` con rollback, `model verify`, Hermes Agent y
  OpenClaw en el sandbox, auditoría completa. Tiempos: instalación < 15 min con imágenes ya
  descargadas; un `upgrade` ~2 min incluida la copia restic.
- La VM tiene reloj propio: si el Mac duerme, Pocket ID puede reiniciarse al despertar
  ("host is not registered"). Las sesiones no se pierden.
- nftables dentro de la VM: la regla de rescate SSH de Guardian cubre la LAN que declares en
  `guardian.yaml`; el SSH de Lima entra por `192.168.5.0/24`, añade esa red a la regla si
  aplicas el firewall del host (`install.sh --with-nftables`).
- x86-64 (mini-PC Intel/AMD): pendiente de prueba; nada en el código depende de la arquitectura,
  pero no lo hemos ejecutado todavía.
