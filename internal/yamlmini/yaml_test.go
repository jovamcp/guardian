package yamlmini

import (
	"reflect"
	"testing"
)

func TestParseYAMLManifest(t *testing.T) {
	src := `
# comentario
name: hello-agent
image: python:3.12-alpine@sha256:abc   # inline
llm:
  models: [qwen2.5:0.5b, "llama3.1:8b"]
  key: auto
egress:
  allow:
    - api.github.com
    - pypi.org
services:
  github:
    url: https://api.github.com
    token: vault:hello/github_token
resources: {cpus: "1", memory: 512m, pids: 128}
command: ["python", "-c", "print(1)"]
schedule:
  cron: ""
empty:
`
	m, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if Str(m["name"]) != "hello-agent" || Str(m["image"]) != "python:3.12-alpine@sha256:abc" {
		t.Fatalf("escalares: %v", m)
	}
	if got := Strs(Map(m["llm"])["models"]); !reflect.DeepEqual(got, []string{"qwen2.5:0.5b", "llama3.1:8b"}) {
		t.Fatalf("lista en línea: %v", got)
	}
	if got := Strs(Map(m["egress"])["allow"]); !reflect.DeepEqual(got, []string{"api.github.com", "pypi.org"}) {
		t.Fatalf("lista en bloque: %v", got)
	}
	if Str(Map(Map(m["services"])["github"])["url"]) != "https://api.github.com" {
		t.Fatalf("url con dos puntos: %v", m["services"])
	}
	if Str(Map(m["resources"])["memory"]) != "512m" || Str(Map(m["resources"])["cpus"]) != "1" {
		t.Fatalf("mapa en línea: %v", m["resources"])
	}
	if got := Strs(m["command"]); !reflect.DeepEqual(got, []string{"python", "-c", "print(1)"}) {
		t.Fatalf("command: %v", got)
	}
	if Str(Map(m["schedule"])["cron"]) != "" || m["empty"] != nil {
		t.Fatalf("vacíos: %v %v", m["schedule"], m["empty"])
	}
}

func TestParseYAMLEscapedQuotesInline(t *testing.T) {
	m, err := Parse(`command: ["python3", "-c", "print(\"hola, mundo\") # no es comentario"]` + "\n")
	if err != nil {
		t.Fatal(err)
	}
	got := Strs(m["command"])
	if len(got) != 3 || got[2] != `print("hola, mundo") # no es comentario` {
		t.Fatalf("comillas escapadas: %q", got)
	}
}

func TestParseYAMLRejectsTabs(t *testing.T) {
	if _, err := Parse("a:\n\tb: 1\n"); err == nil {
		t.Fatal("se esperaba error por tabuladores")
	}
}
