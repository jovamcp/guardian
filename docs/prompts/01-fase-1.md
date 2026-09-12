# Prompt de trabajo — Fase 1

Copia literal de la PARTE B del prompt inicial del proyecto.

---

## PARTE B — Fase 1 (semanas 1–2): base funcional
Objetivo: un usuario clona el repo, ejecuta install.sh y termina con Open WebUI protegido por login OIDC, accesible solo por 443 y por WireGuard, con Ollama inalcanzable desde la LAN. Un commit (o PR) por tarea, en este orden:

1. Compose: confirma con la documentación actual las variables y puertos de Pocket ID, Open WebUI y wg-easy (wg-easy reciente usa asistente web de configuración); corrige y cita la fuente. Healthchecks en todos los servicios.
2. OIDC: consigue que Open WebUI complete el login con Pocket ID (el contenedor debe resolver id.<DOMAIN> vía extra_hosts host-gateway y confiar en la CA de Caddy vía SSL_CERT_FILE/REQUESTS_CA_BUNDLE). Documenta el cliente OIDC y el redirect URI. Cuando funcione, añade ENABLE_LOGIN_FORM=false y verifica que desaparece el login por contraseña.
3. install.sh: pruébalo en VM limpia de Debian 12 y de Ubuntu 24.04; idempotente (segunda ejecución no rompe nada).
4. nftables: valida con nft -c; persistencia (include desde /etc/nftables.conf y servicio habilitado) solo con --with-nftables; implementa en DOCKER-USER las reglas de origen para 443 y 51820 y documenta por qué; la regla de rescate de SSH se queda.
5. guardianctl: implementa init mínimo (exportar la CA de Caddy e imprimir siguientes pasos) y amplía doctor con: Ollama responde solo desde gd_ai (docker compose exec open-webui curl http://ollama:11434/api/tags) y 443 presenta certificado emitido por la CA interna.
6. Docs: docs/instalacion.md completo (DNS local; instalar la CA en iOS, Android, Windows y macOS; primer login; cliente WireGuard con QR), en español y sin suposiciones sobre el nivel del lector.

Criterios de aceptación (verifica y deja evidencia en cada commit):
- nmap -sT -p- <ip-host> desde la LAN muestra solo 443/tcp (y 22 si hay SSH); nmap -sU -p 51820 muestra open|filtered.
- curl http://<ip-host>:11434 desde la LAN falla; desde el contenedor open-webui, http://ollama:11434/api/tags responde.
- Login en https://<DOMAIN> vía Pocket ID funciona; sin login por contraseña.
- Desde un móvil con WireGuard fuera de casa, https://<DOMAIN> carga y el chat funciona.
- make doctor pasa todas las comprobaciones.

Restricciones: no publiques ningún puerto nuevo; no cambies la estructura sin explicarlo; cada cambio en compose indica qué variable cambió y con qué fuente lo verificaste; no toques nada de fases posteriores (agentes, LiteLLM, egreso, auditoría). Si algo del diseño es imposible o inseguro en la práctica, párate, explica el problema y propón la alternativa antes de seguir.
