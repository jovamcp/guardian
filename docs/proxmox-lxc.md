# Guardian en un contenedor LXC de Proxmox

Guardian puede correr dentro de un **LXC de Proxmox VE** en lugar de una VM. Consume menos
recursos y comparte la GPU con más facilidad, a cambio de configurar unas pocas cosas en el
host Proxmox. Si prefieres no tocar nada, una **VM** con Debian 12 funciona sin ajustes.

## Requisitos en el host Proxmox

1. Módulo WireGuard cargado en el **host** (el contenedor no puede cargar módulos):

   ```bash
   modprobe wireguard && echo wireguard >> /etc/modules
   ```

2. Contenedor Debian 12 o Ubuntu 24.04 con **nesting** (Docker dentro de LXC) y **keyctl**:

   ```bash
   pct set <CTID> --features nesting=1,keyctl=1
   ```

3. Dispositivo `tun` para wg-easy. En `/etc/pve/lxc/<CTID>.conf`:

   ```
   lxc.cgroup2.devices.allow: c 10:200 rwm
   lxc.mount.entry: /dev/net/tun dev/net/tun none bind,create=file
   ```

4. **AppArmor** visible dentro del contenedor. Con contenedores **privilegiados** basta con
   `lxc.apparmor.profile: unconfined` (o el perfil por defecto de Proxmox con nesting).
   Con contenedores **no privilegiados**, Docker y el sandbox funcionan, pero `apparmor_parser`
   no puede cargar el perfil `guardian-agent` desde dentro: `agent run` se detendrá. Opciones:
   usa un contenedor privilegiado para Guardian, o una VM.

5. GPU (opcional): pasa `/dev/dri` (AMD/Intel) o instala el driver NVIDIA en el host y expón
   `/dev/nvidia*` con `lxc.cgroup2.devices.allow` y `lxc.mount.entry`. En el compose,
   descomenta el bloque correspondiente de `ollama`.

Reinicia el contenedor tras cambiar la configuración.

## Dentro del contenedor

Igual que en una VM: `sudo ./install.sh`, `guardianctl init`, etc. Comprobaciones específicas:

```bash
ls -l /dev/net/tun          # debe existir
grep -w wireguard /proc/modules   # cargado (desde el host)
ls /sys/kernel/security/apparmor  # visible
sudo make doctor            # la comprobación "entorno de ejecución" detecta LXC y lo valida
```

`nftables` dentro de un LXC funciona con `--with-nftables` si el contenedor tiene
`nesting=1`; las reglas del host Proxmox no se tocan.

## Limitaciones conocidas

- gVisor (`--with-gvisor`) necesita `/dev/kvm` para la plataforma KVM; dentro de LXC usa la
  plataforma `systrap` (por defecto), que funciona en contenedores privilegiados.
- Los saltos de reloj del host afectan al contenedor: si Pocket ID se reinicia con "host is not
  registered", sincroniza la hora del host Proxmox (`chrony`).
