# sandbox/ — perfil de ejecución de agentes

Todo agente arranca con `guardianctl agent run <nombre>` (o `sandbox/runner.sh`), que aplica
el perfil de DESIGN.md §5 sobre `docker run`:

| Control | Cómo |
|---|---|
| Sin root | `--user 10000:10000` (uid fijo, sin entrada en /etc/passwd) |
| Sin capacidades ni escalada | `--cap-drop ALL --security-opt no-new-privileges` |
| Sistema de archivos | `--read-only`, `--tmpfs /tmp` y `--tmpfs /home/agent` (`nosuid,nodev,noexec`) |
| Syscalls | `--security-opt seccomp=sandbox/seccomp-agent.json` |
| MAC | `--security-opt apparmor=guardian-agent` (perfil en `apparmor/`, cargado por el runner) |
| Red | solo `gd_agents` (internal), IP fija, `--dns 172.28.30.53` (Blocky), `HTTPS_PROXY` → Squid |
| Recursos | `--cpus`, `--memory`, `--pids-limit` del manifiesto |
| Secretos | archivos `0400` en `/run/guardian/secrets/` (tmpfs), nunca variables de entorno |
| Docker | sin socket montado; el perfil AppArmor además lo deniega |

## seccomp-agent.json

Parte del perfil por defecto de Docker (`github.com/moby/profiles/seccomp/default.json`) y quita:
los grupos condicionados a capacidades (el agente no tiene ninguna) y `ptrace`, `process_vm_*`,
`bpf`, `perf_event_open`, `unshare`/`setns`, `mount`/`umount*`/`pivot_root`/`chroot`, `mknod*`,
`userfaultfd`, `io_uring_*`, `keyctl`/`add_key`/`request_key`, `set*uid`/`set*gid`, reloj y
módulos. Receta para regenerarlo: descarga el `default.json` de moby/profiles y aplica la lista
anterior (script en el mensaje del commit que lo introdujo).

## apparmor/guardian-agent

Derivado de `docker-default` (ABI 3.0): sin `ptrace`, sin sockets `raw`/`packet`, sin `mount`,
sin escritura en `/proc` ni `/sys`, y deniega el socket de Docker aunque alguien lo montara.
Requiere AppArmor activo en el host (Debian 12 y Ubuntu 24.04 lo traen). Si el kernel no lo
soporta, `agent run` se detiene y lo dice; no hay modo "sin AppArmor" en v0.1.
