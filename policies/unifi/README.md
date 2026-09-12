# UniFi — política de la zona de IA (pendiente)

Estado: **pendiente** (previsto para v0.2). Aplicará la misma política que
`policies/fortios/ai-zone.tmpl` mediante reglas de firewall / Zone-Based Firewall de UniFi:

| Regla | Origen → Destino | Servicio | Acción |
|---|---|---|---|
| LAN-to-Guardian-HTTPS | LAN → host Guardian | 443/tcp | permitir |
| WAN-to-Guardian-WG | Internet → host Guardian (port-forward) | 51820/udp | permitir |
| AI-to-LAN-DENY | red IA → LAN | todo | bloquear + log |
| AI-to-Internet | red IA → Internet | HTTPS, DNS, NTP | permitir |

Requiere una red/VLAN dedicada para la zona de IA (VLAN 20 en el ejemplo).
