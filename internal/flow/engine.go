package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Run states.
const (
	StatusRunning   = "running"
	StatusPaused    = "paused"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Need is what a paused run is waiting for.
type Need struct {
	Var    string `json:"var"`
	Prompt string `json:"prompt"`
}

// Event is one line of what a run did, kept so a finished run can be read
// back and explained. Values are redacted before they land here.
type Event struct {
	Step   string `json:"step"`
	Type   string `json:"type"`
	Detail string `json:"detail,omitempty"`
	Code   int    `json:"code,omitempty"`
}

// Run is one execution. It is a value, saved after every advance, so a paused
// run survives the process that started it -- which is what makes a
// collect_input step work in a non-interactive tool at all.
type Run struct {
	ID       string            `json:"id,omitempty"`
	Flow     string            `json:"flow"`
	FlowID   string            `json:"flow_id,omitempty"`
	Status   string            `json:"status"`
	Cursor   string            `json:"cursor,omitempty"`
	Data     map[string]string `json:"data"`
	Messages []string          `json:"messages,omitempty"`
	Trace    []Event           `json:"trace,omitempty"`
	Needs    *Need             `json:"needs,omitempty"`
	Error    string            `json:"error,omitempty"`
	Thread   string            `json:"thread,omitempty"`

	// The cost of this run, which is the whole argument for flows existing.
	Steps    int `json:"steps"`
	LLMCalls int `json:"llm_calls"`
	Tokens   int `json:"tokens"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Asker performs an ask step. It is a function rather than an interface so
// this package stays unaware of agents, threads and providers: a flow
// knows it wants an answer, not how one is produced.
type Asker func(ctx context.Context, agent, prompt, thread string) (text, threadID string, tokens int, err error)

// Scripter runs a bkn script as the caller. Like Asker it is a function, so
// this package needs to know nothing about bkn, tokens or HTTP. It returns
// the script's value as JSON text plus the run status, so a flow can branch
// on a script that reported failure without the step itself erroring.
type Scripter func(ctx context.Context, name string, input any) (value string, ok bool, err error)

// Engine advances a run.
type Engine struct {
	Flow     *Flow
	Limits   Limits
	Secrets  map[string]string
	Ask      Asker
	Script   Scripter
	MaxSteps int
}

// NewRun starts a run with any inputs already supplied.
func NewRun(f *Flow, inputs map[string]string) *Run {
	data := map[string]string{}
	for k, v := range inputs {
		data[k] = v
	}
	return &Run{Flow: f.Name, FlowID: f.ID, Status: StatusRunning, Cursor: f.Start, Data: data}
}

// Start advances a fresh run until it ends, pauses for input, or fails.
//
// Nothing here returns an error: a failed run is an outcome recorded on the
// run, not an engine failure. The caller still has a run to store, read and
// explain, which is the difference between a flow that failed and a flow that
// vanished.
func (e *Engine) Start(ctx context.Context, run *Run) { e.run(ctx, run) }

// Resume continues a run, applying any newly supplied inputs first.
func (e *Engine) Resume(ctx context.Context, run *Run, inputs map[string]string) {
	for k, v := range inputs {
		run.Data[k] = v
	}
	if run.Status == StatusPaused {
		// The pause is cleared before stepping, so a resume that still lacks
		// the value pauses again on the same step rather than looping.
		run.Needs = nil
		run.Status = StatusRunning
	}
	e.run(ctx, run)
}

func (e *Engine) run(ctx context.Context, run *Run) {
	max := e.MaxSteps
	if max <= 0 {
		max = 100
	}
	for run.Status == StatusRunning {
		// Checked between steps rather than only inside one: a flow can be
		// slow because of many quick steps as easily as one long one, and the
		// step timeout cannot see the difference.
		if err := ctx.Err(); err != nil {
			e.fail(run, fmt.Sprintf("flow timed out after %d steps: %v", run.Steps, err))
			return
		}
		if run.Steps >= max {
			e.fail(run, fmt.Sprintf("stopped after %d steps; the flow may cycle", max))
			return
		}
		step, ok := e.Flow.Steps[run.Cursor]
		if !ok {
			e.fail(run, "step "+run.Cursor+" does not exist")
			return
		}
		run.Steps++
		e.do(ctx, run, run.Cursor, step)
	}
}

func (e *Engine) renderer(run *Run) Renderer {
	return Renderer{Data: run.Data, Secrets: e.Secrets}
}

func (e *Engine) fail(run *Run, msg string) {
	run.Status = StatusFailed
	run.Error = msg
}

func (e *Engine) trace(run *Run, name, typ, detail string, code int) {
	run.Trace = append(run.Trace, Event{Step: name, Type: typ, Detail: detail, Code: code})
}

// goTo moves the cursor, ending the run when a step has nowhere to go.
func (e *Engine) goTo(run *Run, next string) {
	if next == "" {
		run.Status = StatusCompleted
		return
	}
	run.Cursor = next
}

func (e *Engine) do(ctx context.Context, run *Run, name string, s Step) {
	r := e.renderer(run)
	switch s.Type {
	case TypeSet:
		v, err := r.Render(s.Value)
		if err != nil {
			e.fail(run, name+": "+err.Error())
			return
		}
		run.Data[s.Var] = v
		e.trace(run, name, s.Type, s.Var, 0)
		e.goTo(run, s.Next)

	case TypeMessage:
		text, err := r.Render(s.Text)
		if err != nil {
			e.fail(run, name+": "+err.Error())
			return
		}
		run.Messages = append(run.Messages, text)
		e.trace(run, name, s.Type, "", 0)
		e.goTo(run, s.Next)

	case TypeCollect:
		if v, ok := run.Data[s.Var]; ok && (!s.Required || strings.TrimSpace(v) != "") {
			e.trace(run, name, s.Type, s.Var+"=supplied", 0)
			e.goTo(run, s.Next)
			return
		}
		prompt, err := r.Render(s.Prompt)
		if err != nil {
			e.fail(run, name+": "+err.Error())
			return
		}
		// Pausing is how a wizard works in a tool that must never prompt: the
		// run stops, says what it needs, and the caller resumes it with the
		// value. Nothing blocks on a terminal that may not exist.
		run.Status = StatusPaused
		run.Needs = &Need{Var: s.Var, Prompt: prompt}
		e.trace(run, name, s.Type, "waiting for "+s.Var, 0)

	case TypeCond:
		for _, b := range s.When {
			have := run.Data[b.Var]
			want, err := r.Render(b.Value)
			if err != nil {
				e.fail(run, name+": "+err.Error())
				return
			}
			hit, err := compare(have, b.Op, want)
			if err != nil {
				e.fail(run, name+": "+err.Error())
				return
			}
			if hit {
				e.trace(run, name, s.Type, b.Var+" "+b.Op, 0)
				e.goTo(run, b.Next)
				return
			}
		}
		e.trace(run, name, s.Type, "else", 0)
		if s.Else == "" {
			// No else and nothing matched: the flow said nothing about this
			// case, so the run ends rather than guessing at one.
			run.Status = StatusCompleted
			return
		}
		e.goTo(run, s.Else)

	case TypeCmd, TypeHTTP:
		e.doIO(ctx, run, name, s, r)

	case TypeScript:
		e.doScript(ctx, run, name, s, r)

	case TypeAsk:
		e.doAsk(ctx, run, name, s, r)

	case TypeEnd:
		if s.Status == StatusFailed {
			e.fail(run, "flow ended at "+name)
			return
		}
		run.Status = StatusCompleted
		e.trace(run, name, s.Type, "", 0)

	default:
		e.fail(run, name+": unknown step type "+s.Type)
	}
}

func (e *Engine) doIO(ctx context.Context, run *Run, name string, s Step, r Renderer) {
	var out *Output
	var err error
	if s.Type == TypeCmd {
		var args []string
		args, err = r.RenderAll(s.Args)
		if err == nil {
			var stdin string
			if stdin, err = r.Render(s.Stdin); err == nil {
				out, err = RunCmd(ctx, e.Limits, s.Command, args, stdin)
			}
		}
	} else {
		var rawURL, body string
		headers := map[string]string{}
		if rawURL, err = r.Render(s.URL); err == nil {
			if body, err = r.Render(s.Body); err == nil {
				for k, v := range s.Headers {
					var hv string
					if hv, err = r.Render(v); err != nil {
						break
					}
					headers[k] = hv
				}
			}
		}
		if err == nil {
			out, err = RunHTTP(ctx, e.Limits, s.Method, rawURL, headers, body)
		}
	}
	if err != nil {
		// A refused command, an unreachable host or an unset variable is a
		// step that never ran; on_error still catches it, so a flow can
		// handle a missing integration without stopping.
		e.trace(run, name, s.Type, r.Redact(err.Error()), 0)
		e.after(run, name, s, r.Redact(err.Error()), -1)
		return
	}
	if perr := ParseOutput(out, s.Parse); perr != nil {
		e.trace(run, name, s.Type, perr.Error(), out.Code)
		e.after(run, name, s, perr.Error(), out.Code)
		return
	}
	value := strings.TrimRight(out.Text, "\n")
	if s.Select != "" {
		v, ok := Field(out.Parsed, s.Select)
		if !ok {
			msg := "no field " + s.Select + " in the output"
			e.trace(run, name, s.Type, msg, out.Code)
			e.after(run, name, s, msg, out.Code)
			return
		}
		value = v
	}
	if s.Var != "" {
		run.Data[s.Var] = value
		// The exit code and stderr are variables too, because branching on a
		// typed exit code is the point of running a cli-output-spec tool.
		run.Data[s.Var+"_code"] = strconv.Itoa(out.Code)
		run.Data[s.Var+"_err"] = r.Redact(out.Err)
	}
	e.trace(run, name, s.Type, "", out.Code)
	// What counts as failure differs by kind, and conflating them in one
	// expression is how a 200 gets treated as an error somewhere down the
	// line: a process fails when its exit code is non-zero, a request fails
	// when its status is 4xx or 5xx.
	failed := out.Code != 0
	if s.Type == TypeHTTP {
		failed = out.Code >= 400
	}
	if failed {
		e.after(run, name, s, r.Redact(out.Err), out.Code)
		return
	}
	e.goTo(run, s.Next)
}

// after routes a failed step: to on_error when the flow declared one, and to
// a failed run when it did not. A flow that ignores a failure would carry on
// with an unset variable, which is how a wrong answer gets produced
// confidently.
func (e *Engine) after(run *Run, name string, s Step, msg string, code int) {
	if s.OnError != "" {
		if s.Var != "" {
			run.Data[s.Var+"_code"] = strconv.Itoa(code)
			run.Data[s.Var+"_err"] = msg
		}
		e.goTo(run, s.OnError)
		return
	}
	e.fail(run, name+": "+msg)
}

// doScript runs a bkn script through the caller's own token.
func (e *Engine) doScript(ctx context.Context, run *Run, name string, s Step, r Renderer) {
	if e.Script == nil {
		e.fail(run, name+": this run has no way to reach bkn")
		return
	}
	var input any = map[string]any{}
	if s.Input != "" {
		rendered, err := r.Render(s.Input)
		if err != nil {
			e.fail(run, name+": "+err.Error())
			return
		}
		if err := json.Unmarshal([]byte(rendered), &input); err != nil {
			e.fail(run, name+": input is not a JSON object: "+err.Error())
			return
		}
	}
	value, ok, err := e.Script(ctx, s.Script, input)
	if err != nil {
		msg := r.Redact(err.Error())
		e.trace(run, name, s.Type, msg, 0)
		e.after(run, name, s, msg, -1)
		return
	}
	out := &Output{Text: value}
	if !ok {
		// A script that ran and reported failure is data, like a non-zero
		// exit code: the flow may have an on_error branch for exactly this.
		out.Code = 1
		out.Err = "the script reported failure"
	}
	if perr := ParseOutput(out, s.Parse); perr != nil {
		e.trace(run, name, s.Type, perr.Error(), out.Code)
		e.after(run, name, s, perr.Error(), out.Code)
		return
	}
	result := value
	if s.Select != "" {
		v, found := Field(out.Parsed, s.Select)
		if !found {
			msg := "no field " + s.Select + " in the script's value"
			e.trace(run, name, s.Type, msg, out.Code)
			e.after(run, name, s, msg, out.Code)
			return
		}
		result = v
	}
	if s.Var != "" {
		run.Data[s.Var] = result
		run.Data[s.Var+"_code"] = strconv.Itoa(out.Code)
		run.Data[s.Var+"_err"] = out.Err
	}
	e.trace(run, name, s.Type, s.Script, out.Code)
	if !ok {
		e.after(run, name, s, out.Err, out.Code)
		return
	}
	e.goTo(run, s.Next)
}

func (e *Engine) doAsk(ctx context.Context, run *Run, name string, s Step, r Renderer) {
	if e.Ask == nil {
		e.fail(run, name+": this run has no way to reach a model")
		return
	}
	prompt, err := r.Render(s.Prompt)
	if err != nil {
		e.fail(run, name+": "+err.Error())
		return
	}
	text, thread, tokens, err := e.Ask(ctx, s.Agent, prompt, run.Thread)
	if err != nil {
		e.trace(run, name, s.Type, err.Error(), 0)
		e.after(run, name, s, err.Error(), 0)
		return
	}
	run.Thread = thread
	run.LLMCalls++
	run.Tokens += tokens
	if s.Var != "" {
		run.Data[s.Var] = text
	}
	e.trace(run, name, s.Type, s.Agent, 0)
	e.goTo(run, s.Next)
}
