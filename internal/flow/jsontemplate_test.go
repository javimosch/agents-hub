package flow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// posted runs one http step against a recording server. It returns the bodies
// that arrived, so "nothing was sent" is observable.
func posted(t *testing.T, body string, headers, data map[string]string) ([]string, *Run) {
	t.Helper()
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, string(b))
	}))
	t.Cleanup(srv.Close)
	f := &Flow{Name: "f", Start: "s", Steps: map[string]Step{
		"s": {Type: TypeHTTP, Method: "POST", URL: srv.URL, Headers: headers, Body: body, Next: "e"},
		"e": {Type: TypeEnd},
	}}
	e := &Engine{Flow: f, Limits: Limits{Hosts: []string{"127.0.0.1"}},
		Secrets: map[string]string{"TOK": `se"cret`}}
	run := NewRun(f, data)
	e.Start(context.Background(), run)
	return got, run
}

func decoded(t *testing.T, raw string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("sent invalid JSON %q: %v", raw, err)
	}
	return v
}

func TestAValueInsideAJSONStringStaysText(t *testing.T) {
	values := []string{`pa"ss`, `x","role":"admin`, `back\slash\`, "two\nlines\ttab", `<a&b>`,
		`{{secret.TOK}}`, "é ✓", ""}
	for _, v := range values {
		t.Run(v, func(t *testing.T) {
			got, run := posted(t, `{"p":"{{v}}","k":"a \"quoted\" {{v}} here"}`, nil, map[string]string{"v": v})
			if len(got) != 1 {
				t.Fatalf("sent %d requests (%s)", len(got), run.Error)
			}
			want := map[string]any{"p": v, "k": `a "quoted" ` + v + ` here`}
			if have := decoded(t, got[0]); !reflect.DeepEqual(have, want) {
				t.Errorf("got %v, want %v", have, want)
			}
		})
	}
}

func TestASecretInsideAJSONStringStaysText(t *testing.T) {
	got, run := posted(t, `{"t":"{{secret.TOK}}"}`, nil, nil)
	if len(got) != 1 {
		t.Fatalf("sent %d requests (%s)", len(got), run.Error)
	}
	if have := decoded(t, got[0])["t"]; have != `se"cret` {
		t.Errorf("got %q", have)
	}
}

// Outside quotes there is nothing to escape into, so only one plain value fits.
func TestAValueOutsideQuotesMustBeOneScalar(t *testing.T) {
	for _, v := range []string{"3", "-1.5e2", "true", "null", `"text"`, " 7 "} {
		got, run := posted(t, `{"n":{{v}}}`, nil, map[string]string{"v": v})
		if len(got) != 1 {
			t.Errorf("%q: refused (%s)", v, run.Error)
		}
	}
	for _, v := range []string{`1,"role":"admin"`, `{"a":1}`, `[1]`, "abc", "", "1 2"} {
		got, run := posted(t, `{"n":{{v}}}`, nil, map[string]string{"v": v})
		if len(got) != 0 || run.Status != StatusFailed {
			t.Errorf("%q: sent %q, status %s; want nothing sent and a failed run", v, got, run.Status)
		}
	}
}

// A body that is nothing but one value has no structure to break out of.
func TestABodyThatIsOneWholeValueIsSentAsItIs(t *testing.T) {
	got, run := posted(t, " {{doc}} ", nil, map[string]string{"doc": `{"a":[1,2]}`})
	if len(got) != 1 || got[0] != ` {"a":[1,2]} ` {
		t.Errorf("got %q (%s)", got, run.Error)
	}
}

func TestOnlyJSONBodiesAreEscaped(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		headers map[string]string
		want    string
	}{
		{"declared text", `say "{{v}}"`, map[string]string{"Content-Type": "text/plain"}, `say "a"b"`},
		{"declared text that looks like JSON", `{"p":"{{v}}"}`, map[string]string{"content-type": "text/plain"}, `{"p":"a"b"}`},
		{"undeclared plain text", `v={{v}}`, nil, `v=a"b`},
		{"declared JSON with a suffix type", `"{{v}}"`, map[string]string{"Content-Type": "application/merge-patch+json"}, `"a\"b"`},
		{"undeclared array", `["{{v}}"]`, nil, `["a\"b"]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, run := posted(t, c.body, c.headers, map[string]string{"v": `a"b`})
			if len(got) != 1 || got[0] != c.want {
				t.Errorf("got %q (%s), want %q", got, run.Error, c.want)
			}
		})
	}
}

func TestAScriptInputKeepsValuesAsText(t *testing.T) {
	var got []any
	f := &Flow{Name: "f", Start: "s", Steps: map[string]Step{
		"s": {Type: TypeScript, Script: "x", Input: `{"note":"{{v}}"}`, Next: "e"},
		"e": {Type: TypeEnd},
	}}
	e := &Engine{Flow: f, Script: func(_ context.Context, _ string, in any) (string, bool, error) {
		got = append(got, in)
		return "{}", true, nil
	}}
	run := NewRun(f, map[string]string{"v": `x","admin":"yes`})
	e.Start(context.Background(), run)
	want := []any{map[string]any{"note": `x","admin":"yes`}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("script got %v (%s), want %v", got, run.Error, want)
	}
}
