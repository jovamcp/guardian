# sandbox/ — perfil de ejecución de agentes (Fase 2)

Este directorio está vacío en la Fase 1. En la Fase 2 contendrá:

- `seccomp-agent.json`: perfil seccomp restrictivo para los contenedores de agentes
  (sin `ptrace`, `mount`, `keyctl`, `bpf`, `unshare`, etc.).
- `apparmor/guardian-agent`: perfil AppArmor cargado en el host (`apparmor_parser -r`).
- `runner.sh`: lanza un agente a partir de su manifiesto (`agents/examples/*.yaml`) con
  `--user`, `--read-only`, `--tmpfs`, `--cap-drop ALL`, `--security-opt no-new-privileges`,
  `--security-opt seccomp=…`, `--security-opt apparmor=…`, red `gd_agents`, DNS y
  `HTTPS_PROXY` de Guardian y secretos inyectados como archivos `0400`.

El diseño completo (manifiesto y perfil de ejecución) está en `DESIGN.md`, sección 5.
