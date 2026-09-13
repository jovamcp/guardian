// Analizador YAML mínimo para los manifiestos de agentes (solo biblioteca estándar).
//
// Subconjunto soportado, suficiente para agents/*.yaml:
//   - mapas por indentación (espacios), listas con "- item" y listas en línea "[a, b]"
//   - escalares: cadenas (con o sin comillas simples/dobles), enteros, flotantes, true/false, null
//   - comentarios con "#" y líneas vacías
//
// No soporta anclas, documentos múltiples, bloques literales "|" ni claves complejas.
package yamlmini

import (
	"bufio"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type yamlLine struct {
	indent int
	text   string
	num    int
}

func Parse(src string) (map[string]any, error) {
	var lines []yamlLine
	sc := bufio.NewScanner(strings.NewReader(src))
	n := 0
	for sc.Scan() {
		n++
		raw := sc.Text()
		if strings.Contains(raw, "\t") {
			return nil, fmt.Errorf("línea %d: tabuladores no permitidos en YAML", n)
		}
		txt := stripComment(raw)
		if strings.TrimSpace(txt) == "" {
			continue
		}
		indent := len(txt) - len(strings.TrimLeft(txt, " "))
		lines = append(lines, yamlLine{indent, strings.TrimSpace(txt), n})
	}
	p := &yamlParser{lines: lines}
	v, err := p.parseBlock(0)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("el documento raíz debe ser un mapa")
	}
	return m, nil
}

type yamlParser struct {
	lines []yamlLine
	pos   int
}

func (p *yamlParser) parseBlock(indent int) (any, error) {
	if p.pos >= len(p.lines) {
		return map[string]any{}, nil
	}
	if strings.HasPrefix(p.lines[p.pos].text, "- ") || p.lines[p.pos].text == "-" {
		return p.parseList(indent)
	}
	return p.parseMap(indent)
}

func (p *yamlParser) parseMap(indent int) (map[string]any, error) {
	m := map[string]any{}
	for p.pos < len(p.lines) {
		ln := p.lines[p.pos]
		if ln.indent < indent {
			break
		}
		if ln.indent > indent {
			return nil, fmt.Errorf("línea %d: indentación inesperada", ln.num)
		}
		key, rest, ok := splitKey(ln.text)
		if !ok {
			return nil, fmt.Errorf("línea %d: se esperaba 'clave: valor'", ln.num)
		}
		p.pos++
		if rest == "" {
			// Valor anidado (mapa o lista) en las líneas siguientes.
			if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
				v, err := p.parseBlock(p.lines[p.pos].indent)
				if err != nil {
					return nil, err
				}
				m[key] = v
			} else if p.pos < len(p.lines) && p.lines[p.pos].indent == indent && strings.HasPrefix(p.lines[p.pos].text, "- ") {
				// Lista al mismo nivel que la clave (estilo permitido en YAML).
				v, err := p.parseList(indent)
				if err != nil {
					return nil, err
				}
				m[key] = v
			} else {
				m[key] = nil
			}
			continue
		}
		v, err := parseScalarOrInline(rest, ln.num)
		if err != nil {
			return nil, err
		}
		m[key] = v
	}
	return m, nil
}

func (p *yamlParser) parseList(indent int) ([]any, error) {
	var out []any
	for p.pos < len(p.lines) {
		ln := p.lines[p.pos]
		if ln.indent < indent || !(strings.HasPrefix(ln.text, "- ") || ln.text == "-") {
			break
		}
		if ln.indent > indent {
			return nil, fmt.Errorf("línea %d: indentación inesperada en lista", ln.num)
		}
		item := strings.TrimSpace(strings.TrimPrefix(ln.text, "-"))
		p.pos++
		if item == "" {
			v, err := p.parseBlock(p.lines[p.pos].indent)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		if k, rest, ok := splitKey(item); ok && !strings.HasPrefix(item, "[") && !strings.HasPrefix(item, "\"") && !strings.HasPrefix(item, "'") {
			// "- clave: valor" → mapa dentro de la lista; las claves siguientes van indentadas +2.
			m := map[string]any{}
			if rest == "" {
				if p.pos < len(p.lines) && p.lines[p.pos].indent > indent+1 {
					v, err := p.parseBlock(p.lines[p.pos].indent)
					if err != nil {
						return nil, err
					}
					m[k] = v
				}
			} else {
				v, err := parseScalarOrInline(rest, ln.num)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			if p.pos < len(p.lines) && p.lines[p.pos].indent == indent+2 {
				more, err := p.parseMap(indent + 2)
				if err != nil {
					return nil, err
				}
				for kk, vv := range more {
					m[kk] = vv
				}
			}
			out = append(out, m)
			continue
		}
		v, err := parseScalarOrInline(item, ln.num)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// splitKey separa "clave: resto" respetando comillas y URLs (http://…).
func splitKey(s string) (string, string, bool) {
	if strings.HasPrefix(s, "\"") || strings.HasPrefix(s, "'") {
		q := s[0]
		end := strings.IndexByte(s[1:], q)
		if end < 0 {
			return "", "", false
		}
		key := s[1 : end+1]
		rest := strings.TrimSpace(s[end+2:])
		if !strings.HasPrefix(rest, ":") {
			return "", "", false
		}
		return key, strings.TrimSpace(rest[1:]), true
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ':' && (i+1 == len(s) || s[i+1] == ' ') {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), true
		}
	}
	return "", "", false
}

func stripComment(s string) string {
	inS, inD := false, false
	for i := 0; i < len(s); i++ {
		if inD && s[i] == '\\' {
			i++ // carácter escapado dentro de comillas dobles
			continue
		}
		switch s[i] {
		case '\'':
			if !inD {
				inS = !inS
			}
		case '"':
			if !inS {
				inD = !inD
			}
		case '#':
			if !inS && !inD && (i == 0 || s[i-1] == ' ') {
				return s[:i]
			}
		}
	}
	return s
}

func parseScalarOrInline(s string, num int) (any, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		if !strings.HasSuffix(s, "]") {
			return nil, fmt.Errorf("línea %d: lista en línea sin cerrar", num)
		}
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			return []any{}, nil
		}
		var out []any
		for _, part := range splitCSV(inner) {
			v, err := parseScalarOrInline(part, num)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	if strings.HasPrefix(s, "{") {
		if !strings.HasSuffix(s, "}") {
			return nil, fmt.Errorf("línea %d: mapa en línea sin cerrar", num)
		}
		inner := strings.TrimSpace(s[1 : len(s)-1])
		m := map[string]any{}
		if inner == "" {
			return m, nil
		}
		for _, part := range splitCSV(inner) {
			k, rest, ok := splitKey(strings.TrimSpace(part))
			if !ok {
				return nil, fmt.Errorf("línea %d: mapa en línea inválido", num)
			}
			v, err := parseScalarOrInline(rest, num)
			if err != nil {
				return nil, err
			}
			m[k] = v
		}
		return m, nil
	}
	return parseScalar(s), nil
}

func splitCSV(s string) []string {
	var parts []string
	depth := 0
	inS, inD := false, false
	start := 0
	for i := 0; i < len(s); i++ {
		if inD && s[i] == '\\' {
			i++ // carácter escapado dentro de comillas dobles
			continue
		}
		switch s[i] {
		case '\'':
			if !inD {
				inS = !inS
			}
		case '"':
			if !inS {
				inD = !inD
			}
		case '[', '{':
			if !inS && !inD {
				depth++
			}
		case ']', '}':
			if !inS && !inD {
				depth--
			}
		case ',':
			if !inS && !inD && depth == 0 {
				parts = append(parts, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	parts = append(parts, strings.TrimSpace(s[start:]))
	return parts
}

func parseScalar(s string) any {
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		inner := s[1 : len(s)-1]
		if s[0] == '"' {
			inner = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n").Replace(inner)
		}
		return inner
	}
	switch s {
	case "true", "True", "yes":
		return true
	case "false", "False", "no":
		return false
	case "null", "~", "":
		return nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && strings.ContainsAny(s, ".eE") {
		return f
	}
	return s
}

// Accesores tolerantes para navegar el resultado.
func Map(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func Str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func Strs(v any) []string {
	l, ok := v.([]any)
	if !ok {
		if s := Str(v); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(l))
	for _, x := range l {
		out = append(out, Str(x))
	}
	return out
}
