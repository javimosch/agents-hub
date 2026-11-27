package eval

import (
	"fmt"
	"strings"
)

// ParseExpect reads an assertion written as op=value, and refuses one that
// Validate would refuse, before anything is saved.
func ParseExpect(s string) (Expect, error) {
	op, value, ok := strings.Cut(s, "=")
	if !ok || value == "" {
		return Expect{}, fmt.Errorf("%q is not op=value", s)
	}
	e := Expect{Op: op, Value: value}
	probe := &Scenario{Name: "n", Agent: "a", Judge: "j",
		Cases: []Case{{Name: "c", Message: "m", Expect: []Expect{e}}}}
	for _, p := range Validate(probe) {
		if p.Fatal {
			return Expect{}, fmt.Errorf("%q: %s", s, p.Message)
		}
	}
	return e, nil
}
