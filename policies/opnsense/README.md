# OPNsense — política de la zona de IA

`guardianctl policy render opnsense` genera una guía paso a paso (`ai-zone.md.tmpl`) con los
valores de tu `guardian.yaml`: interfaz VLAN, alias, reglas LAN→Guardian (443), AI→LAN
denegado con log, AI→Internet solo HTTPS/DNS/NTP y el port-forward de 51820/udp.

```bash
sudo ./bin/guardianctl policy render opnsense --out /tmp/opnsense-ai-zone.md
```

En v0.1 no se genera XML de importación: OPNsense se configura desde la interfaz siguiendo la guía.
