package eval

import "testing"

func TestAnExpectationIsWrittenAsOpEqualsValue(t *testing.T) {
	cases := map[string]Expect{
		"contains=all good":    {Op: OpContains, Value: "all good"},
		"matches=a=b|c":        {Op: OpMatches, Value: "a=b|c"},
		"tool_used=cmd":        {Op: OpToolUsed, Value: "cmd"},
		"judge=Is it correct?": {Op: OpJudge, Value: "Is it correct?"},
	}
	for in, want := range cases {
		got, err := ParseExpect(in)
		if err != nil || got != want {
			t.Errorf("%q: got %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "contains", "contains=", "nonsense=x", "max_steps=many", "matches=("} {
		if _, err := ParseExpect(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
