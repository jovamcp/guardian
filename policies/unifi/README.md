# UniFi — política de la zona de IA

`guardianctl policy render unifi` genera una guía (`ai-zone.md.tmpl`) con los valores de tu
`guardian.yaml` para UniFi Network con Zone-Based Firewall: red/VLAN de la zona de IA, zona `AI`,
políticas Internal↔AI y AI→External (solo HTTPS/DNS/NTP) y el port-forward de 51820/udp.

```bash
sudo ./bin/guardianctl policy render unifi --out /tmp/unifi-ai-zone.md
```
