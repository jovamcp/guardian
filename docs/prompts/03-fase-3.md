# Prompt de trabajo — Fase 3 (semanas 5–6): auditoría y alertas

Derivado de DESIGN.md (hito 3). Principio 4: **todo flujo deja rastro**.

## Objetivo

Cada login (Pocket ID), cada llamada al LLM (LiteLLM), cada decisión del proxy de egreso (Squid),
cada consulta DNS bloqueada (Blocky) y cada descarte del firewall (nftables) se puede consultar en
Grafana, se conserva `audit.retention_days` días y dispara una alerta en el móvil (ntfy) cuando
un agente intenta salir a un destino no permitido o alguien falla repetidamente el login.

## Tareas (un commit por tarea, en este orden)

1. **Plan y decisiones.** Este archivo, DESIGN.md §11 y CLAUDE.md. Decisiones nuevas:
   - **Vector no toca el socket de Docker directamente**: lee los logs a través de un
     `docker-socket-proxy` (solo `GET` de contenedores/logs/eventos) en una red interna
     `gd_audit`. Un compromiso de Vector no da control del daemon.
   - **Los descartes de nftables** llegan por `journald` (kernel), montado de solo lectura en Vector.
   - **Loki en modo monolítico con almacenamiento en disco**, retención por el compactor.
   - **Grafana detrás de Caddy en `logs.<DOMAIN>` con login OIDC de Pocket ID**; sin formulario
     local salvo el admin de emergencia. Dashboards y datasource **provisionados** desde el repo.
   - **Alertas por webhook a ntfy** (`https://ntfy.sh/<topic>?template=grafana` o servidor
     propio), sin contenedor intermedio: ntfy ya trae plantilla para Grafana.
   - **`schedule.cron`** de los agentes se materializa como timers de systemd generados por
     `guardianctl agent schedule`; el planificador es el host, no un contenedor con socket.
2. **Recolección.** `gd_audit` (internal), `docker-socket-proxy`, Vector (fuentes: docker_logs
   por TCP al proxy + journald del kernel; transformaciones que etiquetan `service`, `agent`,
   `event`), Loki con retención. Verificar: en Loki hay líneas de los 9 servicios y del kernel
   con etiquetas útiles; `curl` a `/loki/api/v1/labels` desde `gd_audit`.
3. **Grafana.** Servicio provisionado (datasource Loki, dashboards "Accesos", "LLM", "Egreso"),
   `logs.<DOMAIN>` en Caddy, OIDC contra Pocket ID (cliente `grafana`, callback
   `/login/generic_oauth`), CA interna montada. Verificar login y que los tres paneles
   muestran datos de la VM.
4. **Alertas.** Contact point ntfy (URL y topic desde `.env`), política por defecto y reglas:
   egreso denegado a un agente (Squid `TCP_DENIED` o Blocky bloqueado) y ≥5 logins fallidos
   en 10 min en Pocket ID. Verificar: provocar un `TCP_DENIED` con `hello-agent` y recibir la
   notificación en el topic (suscripción `curl .../json?poll=1`).
5. **Planificación.** `guardianctl agent schedule apply|list|remove`: convierte `schedule.cron`
   (5 campos) en `OnCalendar` y escribe `/etc/systemd/system/guardian-agent-<n>.{service,timer}`
   que llaman a `sandbox/runner.sh`. Verificar con un timer cada minuto en la VM.
6. **Doctor y docs.** `doctor`: Loki ingiere (consulta reciente no vacía), Grafana healthy,
   contact point configurado. `docs/auditoria.md`, README, CLAUDE.md.

## Criterios de aceptación

- Un login en `https://<DOMAIN>` aparece en el panel "Accesos" en menos de un minuto.
- Una llamada a `api.<DOMAIN>/v1/chat/completions` aparece en "LLM" con el alias de la llave.
- `hello-agent` intentando `example.com` aparece en "Egreso" como denegado y llega una
  notificación ntfy en menos de 2 minutos.
- Los logs se conservan `audit.retention_days` días (retención configurada y verificada en Loki).
- Los puertos publicados siguen siendo solo 443/tcp y 51820/udp; ningún contenedor nuevo con
  el socket de Docker salvo el `docker-socket-proxy` (solo lectura, red interna).
- `make doctor` en verde.

## Restricciones

Las de las fases anteriores. Además: Grafana nunca se publica sin OIDC; el proxy del socket
nunca expone `POST` ni `exec`; los dashboards son archivos del repo (reproducible).
