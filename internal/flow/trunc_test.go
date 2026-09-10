package flow

import "strings"
import "testing"

func TestTruncatedJSONSaysSo(t *testing.T) {
	o := &Output{Text: strings.Repeat("x", 10) , Truncated: true}
	err := ParseOutput(o, "json")
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("want a truncation explanation, got %v", err)
	}
	o2 := &Output{Text: "nope"}
	if err := ParseOutput(o2, "json"); err == nil || strings.Contains(err.Error(), "truncated") {
		t.Fatalf("untruncated garbage should report the syntax error, got %v", err)
	}
}
