---
name: Beta v0.1
about: Reporte de un beta tester (instalación, doctor, tiempos)
labels: beta
---

**Entorno**
- Distribución y versión:
- CPU / arquitectura / GPU:
- Dónde corre (metal, Proxmox, VM, otro):
- Firewall perimetral (FortiGate, OPNsense, UniFi, ninguno):

**Cronómetro**
- `install.sh` → `doctor` en verde: ____ min (o dónde se paró)
- Pasos manuales (DNS, CA, Pocket ID, WireGuard): ____ min

**Qué pasó**
Paso de `docs/instalacion.md` y qué esperabas:

**Salidas** (sin `compose/.env`, `vault/` ni llaves)
```
sudo make doctor
```
```
sudo docker compose --env-file compose/.env -f compose/docker-compose.yml ps
```
