# OPNsense — política de la zona de IA (pendiente)

Estado: **pendiente** (hito 4 de v0.1). Implementará la misma política que
`policies/fortios/ai-zone.tmpl`, exportable como reglas de firewall de OPNsense:

| Regla | Origen → Destino | Servicio | Acción |
|---|---|---|---|
| LAN-to-Guardian-HTTPS | LAN_NET → GUARDIAN_HOST | 443/tcp | permitir + log |
| WAN-to-Guardian-WG | WAN → GUARDIAN_HOST (port-forward) | 51820/udp | permitir + log |
| AI-to-LAN-DENY | AI_ZONE_NET → LAN_NET | todo | denegar + log |
| AI-to-Internet | AI_ZONE_NET → WAN | HTTPS, DNS, NTP | permitir + NAT + log |

Además: interfaz VLAN `ai-zone` (VLAN 20 en el ejemplo) con puerta de enlace en la VLAN.
