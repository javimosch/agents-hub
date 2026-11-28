package flow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// A value put into a JSON template must stay a value. Substituted as text, a
// quote in it breaks the document, and a chosen one adds fields to it.

// wholeValue reports a template that is one placeholder and nothing else. It
// has no structure of its own, so the value is the document.
func wholeValue(tmpl string) bool {
	t := strings.TrimSpace(tmpl)
	loc := varRe.FindStringIndex(t)
	return loc != nil && loc[0] == 0 && loc[1] == len(t)
}

// isJSONBody says whether an http body is JSON: by its declared type, or, when
// none is declared (it is then sent as JSON), by how the template starts.
func isJSONBody(tmpl string, headers map[string]string) bool {
	for k, v := range headers {
		if strings.EqualFold(k, "Content-Type") {
			return strings.Contains(strings.ToLower(v), "json")
		}
	}
	t := strings.TrimSpace(tmpl)
	return strings.HasPrefix(t, "[") || (strings.HasPrefix(t, "{") && !strings.HasPrefix(t, "{{"))
}

// RenderJSON substitutes into a JSON template. Inside a string a value is
// escaped. Outside one it must be a single scalar, since nothing else can be
// put there without changing the document's shape.
func (r Renderer) RenderJSON(tmpl string) (string, error) {
	if wholeValue(tmpl) {
		return r.Render(tmpl)
	}
	var out strings.Builder
	inString, escaped := false, false
	last := 0
	for _, loc := range varRe.FindAllStringIndex(tmpl, -1) {
		for _, c := range []byte(tmpl[last:loc[0]]) {
			switch {
			case escaped:
				escaped = false
			case inString && c == '\\':
				escaped = true
			case c == '"':
				inString = !inString
			}
		}
		out.WriteString(tmpl[last:loc[0]])
		placeholder := tmpl[loc[0]:loc[1]]
		value, err := r.Render(placeholder)
		if err != nil {
			return "", err
		}
		switch {
		case escaped:
			return "", fmt.Errorf("%s follows a backslash in a JSON template", placeholder)
		case inString:
			out.WriteString(escapeJSON(value))
		case isScalar(value):
			out.WriteString(value)
		default:
			// The value is not quoted back: it may be a secret.
			return "", fmt.Errorf("%s is outside quotes in a JSON template, so it must be "+
				"one number, true, false, null or quoted string; write \"%s\" to send it as text",
				placeholder, placeholder)
		}
		last = loc[1]
	}
	out.WriteString(tmpl[last:])
	return out.String(), nil
}

// escapeJSON returns s as the inside of a JSON string.
func escapeJSON(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	quoted := strings.TrimSuffix(b.String(), "\n")
	return quoted[1 : len(quoted)-1]
}

func isScalar(s string) bool {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return false
	}
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}
