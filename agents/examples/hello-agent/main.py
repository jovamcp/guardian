"""hello-agent: prueba de extremo a extremo del sandbox de Guardian (solo biblioteca estándar)."""
import json
import os
import sys
import urllib.error
import urllib.request

def read_secret(name: str) -> str:
    path = os.path.join(os.environ.get("GUARDIAN_SECRETS_DIR", "/run/guardian/secrets"), name)
    with open(path, encoding="utf-8") as f:
        return f.read().strip()

def main() -> int:
    ok = True
    base = os.environ.get("OPENAI_BASE_URL", "http://litellm:4000/v1")
    model = os.environ.get("GUARDIAN_MODEL", "qwen2.5:0.5b")
    key = read_secret("llm_key")

    # 1) LLM a través del gateway, sin proxy (NO_PROXY incluye litellm).
    req = urllib.request.Request(
        f"{base}/chat/completions",
        data=json.dumps({"model": model, "max_tokens": 12,
                         "messages": [{"role": "user", "content": "Responde solo: hola"}]}).encode(),
        headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=180) as r:
            body = json.load(r)
        print("[ok] llm:", body["choices"][0]["message"]["content"].strip()[:60])
    except Exception as e:  # noqa: BLE001
        print("[fail] llm:", e); ok = False

    # 2) Dominio permitido vía proxy (urllib honra HTTPS_PROXY).
    try:
        with urllib.request.urlopen("https://api.github.com/zen", timeout=30) as r:
            print("[ok] egreso permitido api.github.com:", r.status)
    except Exception as e:  # noqa: BLE001
        print("[fail] egreso permitido:", e); ok = False

    # 3) Dominio no permitido: debe fallar (403 del proxy).
    try:
        with urllib.request.urlopen("https://example.com/", timeout=30) as r:
            print("[fail] example.com respondió", r.status, "(debería estar bloqueado)"); ok = False
    except urllib.error.HTTPError as e:
        print("[ok] egreso bloqueado example.com:", e.code)
    except Exception as e:  # noqa: BLE001
        print("[ok] egreso bloqueado example.com:", type(e).__name__)

    # 4) Sin ruta directa: conectar a 1.1.1.1:443 sin proxy debe fallar.
    import socket
    try:
        socket.create_connection(("1.1.1.1", 443), timeout=5).close()
        print("[fail] salida directa a 1.1.1.1:443 abierta"); ok = False
    except OSError as e:
        print("[ok] sin salida directa:", type(e).__name__)

    # 5) Secreto del vault: archivo 0400, nunca variable de entorno.
    tok_file = os.environ.get("GITHUB_TOKEN_FILE", "")
    if tok_file and os.path.exists(tok_file):
        mode = oct(os.stat(tok_file).st_mode & 0o777)
        print(f"[ok] secreto github_token: {len(open(tok_file).read())} bytes, modo {mode}, en entorno: {'GITHUB_TOKEN' in os.environ}")
    else:
        print("[fail] falta el secreto github_token"); ok = False

    print("uid:", os.getuid(), "read-only:", not os.access("/", os.W_OK), "docker.sock:", os.path.exists("/var/run/docker.sock"))
    return 0 if ok else 1

if __name__ == "__main__":
    sys.exit(main())
