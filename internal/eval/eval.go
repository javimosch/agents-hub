// Package eval is the measurement half.
//
// Everything else in this tool changes behaviour: a system prompt, a model, a
// memory budget, a skill, a tool. None of those changes can be judged by
// reading them. An eval is a set of cases with assertions, run against an
// agent, producing a pass rate and a cost -- so "is this better?" becomes a
// question with an answer instead of an opinion.
//
// The assertion set is closed, like the flow operators and bkn's query
// operators before it. Most of them cost nothing to check; exactly one (judge)
// asks a model, and it says so in the report.
package eval

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Assertion operators.
const (
	OpContains    = "contains"
	OpNotContains = "not_contains"
	OpEquals      = "equals"
	OpMatches     = "matches"
	OpMaxTokens   = "max_tokens"
	OpMaxSteps    = "max_steps"
	OpToolUsed    = "tool_used"
	OpToolNotUsed = "tool_not_used"
	OpJudge       = "judge"
)

// Ops lists every operator, cheapest first.
func Ops() []string {
	return []string{OpContains, OpNotContains, OpEquals, OpMatches,
		OpMaxTokens, OpMaxSteps, OpToolUsed, OpToolNotUsed, OpJudge}
}

// Costly reports whether an operator spends tokens to check.
func Costly(op string) bool { return op == OpJudge }

// Expect is one assertion about a case's outcome.
type Expect struct {
	Op    string `json:"op"`
	Value string `json:"value"`
	// Why is an optional note carried into the report, so a failure explains
	// what the assertion was protecting rather than only what it compared.
	Why string `json:"why,omitempty"`
}

// Case is one input and what should be true of the answer.
type Case struct {
	Name    string   `json:"name"`
	Message string   `json:"message"`
	Expect  []Expect `json:"expect"`
	// Skip records a case deliberately parked, with the reason visible in the
	// report. A commented-out case is a case nobody remembers to restore.
	Skip string `json:"skip,omitempty"`
}

// Scenario is a named set of cases run against one agent.
type Scenario struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Agent       string `json:"agent"`
	// Judge names the agent that answers judge assertions. It defaults to the
	// agent under test only if nothing else is set, which is worth avoiding:
	// a model grading its own homework is a weaker signal than a second one.
	Judge string `json:"judge,omitempty"`
	// Tools are offered to every case besides the agent's own. Naming one
	// grants nothing: the machine's command rules and --allow-writes apply.
	Tools     []string `json:"tools,omitempty"`
	Cases     []Case   `json:"cases"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

// Parse reads a scenario definition.
func Parse(raw []byte) (*Scenario, error) {
	var s Scenario
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("not a valid eval scenario: %w", err)
	}
	return &s, nil
}

// Problem is one static finding.
type Problem struct {
	Case    string `json:"case,omitempty"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal"`
}

// Fatal reports whether any problem blocks a run.
func Fatal(ps []Problem) bool {
	for _, p := range ps {
		if p.Fatal {
			return true
		}
	}
	return false
}

// Validate checks a scenario without running it -- including that every regex
// compiles and every numeric bound parses, which are the two mistakes that
// would otherwise only surface after the run had already been paid for.
func Validate(s *Scenario) []Problem {
	var out []Problem
	add := func(c, m string, fatal bool) { out = append(out, Problem{Case: c, Message: m, Fatal: fatal}) }

	if strings.TrimSpace(s.Name) == "" {
		add("", "scenario needs a name", true)
	}
	if strings.TrimSpace(s.Agent) == "" {
		add("", "scenario needs an agent to run against", true)
	}
	if len(s.Cases) == 0 {
		add("", "scenario has no cases", true)
		return out
	}
	seen := map[string]bool{}
	needsJudge := false
	for _, c := range s.Cases {
		if c.Name == "" {
			add("", "every case needs a name", true)
			continue
		}
		if seen[c.Name] {
			add(c.Name, "duplicate case name; a report could not tell them apart", true)
		}
		seen[c.Name] = true
		if strings.TrimSpace(c.Message) == "" && c.Skip == "" {
			add(c.Name, "case needs a message", true)
		}
		if len(c.Expect) == 0 && c.Skip == "" {
			add(c.Name, "case asserts nothing, so it can never fail", false)
		}
		for _, e := range c.Expect {
			switch e.Op {
			case OpMatches:
				if _, err := regexp.Compile(e.Value); err != nil {
					add(c.Name, "matches: "+err.Error(), true)
				}
			case OpMaxTokens, OpMaxSteps:
				if _, err := strconv.Atoi(e.Value); err != nil {
					add(c.Name, e.Op+" needs a number, got "+strconv.Quote(e.Value), true)
				}
			case OpJudge:
				needsJudge = true
				if strings.TrimSpace(e.Value) == "" {
					add(c.Name, "judge needs a question to ask", true)
				}
			case OpContains, OpNotContains, OpEquals, OpToolUsed, OpToolNotUsed:
				if e.Value == "" {
					add(c.Name, e.Op+" needs a value", true)
				}
			case "":
				add(c.Name, "assertion has no op", true)
			default:
				add(c.Name, "unknown op "+e.Op+"; use "+strings.Join(Ops(), ", "), true)
			}
		}
	}
	if needsJudge && s.Judge == "" {
		add("", "this scenario uses judge assertions but names no judge agent; "+
			"the agent under test would grade its own answer", false)
	}
	return out
}

// Outcome is what one case produced, as the assertions need to see it.
type Outcome struct {
	Answer string
	Tokens int
	Steps  int
	Tools  []string
}

// Result is one assertion's verdict.
type Result struct {
	Op     string `json:"op"`
	Value  string `json:"value"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
	Why    string `json:"why,omitempty"`
	Tokens int    `json:"tokens,omitempty"`
}

// Judge answers a judge assertion. It is a function so this package stays
// unaware of agents, threads and providers.
type Judge func(question, answer string) (pass bool, reason string, tokens int, err error)

// Check evaluates one assertion against an outcome.
func Check(e Expect, o Outcome, judge Judge) Result {
	r := Result{Op: e.Op, Value: e.Value, Why: e.Why}
	body := o.Answer
	switch e.Op {
	case OpContains:
		r.Pass = strings.Contains(strings.ToLower(body), strings.ToLower(e.Value))
		if !r.Pass {
			r.Detail = "not found in the answer"
		}
	case OpNotContains:
		r.Pass = !strings.Contains(strings.ToLower(body), strings.ToLower(e.Value))
		if !r.Pass {
			r.Detail = "present in the answer but should not be"
		}
	case OpEquals:
		r.Pass = strings.TrimSpace(body) == strings.TrimSpace(e.Value)
		if !r.Pass {
			r.Detail = "answer was " + truncate(body, 120)
		}
	case OpMatches:
		re, err := regexp.Compile(e.Value)
		if err != nil {
			r.Detail = err.Error()
			return r
		}
		r.Pass = re.MatchString(body)
		if !r.Pass {
			r.Detail = "no match in the answer"
		}
	case OpMaxTokens:
		n, _ := strconv.Atoi(e.Value)
		r.Pass = o.Tokens <= n
		r.Detail = fmt.Sprintf("used %d", o.Tokens)
	case OpMaxSteps:
		n, _ := strconv.Atoi(e.Value)
		r.Pass = o.Steps <= n
		r.Detail = fmt.Sprintf("took %d", o.Steps)
	case OpToolUsed:
		r.Pass = usedTool(o.Tools, e.Value)
		if !r.Pass {
			r.Detail = "tools called: " + listOr(o.Tools, "none")
		}
	case OpToolNotUsed:
		r.Pass = !usedTool(o.Tools, e.Value)
		if !r.Pass {
			r.Detail = "tools called: " + listOr(o.Tools, "none")
		}
	case OpJudge:
		if judge == nil {
			r.Detail = "no judge available"
			return r
		}
		pass, reason, tokens, err := judge(e.Value, body)
		if err != nil {
			r.Detail = "judge failed: " + err.Error()
			return r
		}
		r.Pass, r.Detail, r.Tokens = pass, reason, tokens
	default:
		r.Detail = "unknown op"
	}
	return r
}

func usedTool(tools []string, name string) bool {
	for _, t := range tools {
		if t == name {
			return true
		}
	}
	return false
}

func listOr(items []string, empty string) string {
	if len(items) == 0 {
		return empty
	}
	sorted := append([]string{}, items...)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return strconv.Quote(s)
	}
	return strconv.Quote(s[:n]) + "..."
}
