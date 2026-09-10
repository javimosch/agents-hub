package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// maxOut caps what a command or response may contribute to a run. A run
// record lives in a database and may be replayed into a prompt; an unbounded
// capture is a bill and a storage problem at the same time.
const maxOut = 256 << 10

// Prefix is one command shape an operator has permitted.
//
// A prefix rather than a bare command name, because "which program" is rarely
// the interesting question. Allowing "sudo" would allow sudo ANYTHING; what an
// operator means is "journalctl, as root" -- three fields, not one. A
// single-word prefix behaves exactly as a bare name always did.
type Prefix struct {
	Raw    string
	Fields []string
}

// ParsePrefixes turns configured strings into prefixes, refusing any that is
// empty or starts with a flag.
func ParsePrefixes(raw []string) ([]Prefix, error) {
	out := make([]Prefix, 0, len(raw))
	for _, r := range raw {
		fields := strings.Fields(r)
		if len(fields) == 0 {
			continue
		}
		if strings.HasPrefix(fields[0], "-") {
			return nil, fmt.Errorf("%q does not start with a command", r)
		}
		out = append(out, Prefix{Raw: strings.Join(fields, " "), Fields: fields})
	}
	return out, nil
}

// Match reports whether an argv begins with one of the permitted prefixes.
// Comparison is on whole fields, so "git logger" is not "git log".
func Match(prefixes []Prefix, argv []string) (Prefix, bool) {
	for _, p := range prefixes {
		if len(argv) < len(p.Fields) {
			continue
		}
		same := true
		for i := range p.Fields {
			if argv[i] != p.Fields[i] {
				same = false
				break
			}
		}
		if same {
			return p, true
		}
	}
	return Prefix{}, false
}

// Names renders prefixes for an error message.
func Names(prefixes []Prefix) string {
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, p.Raw)
	}
	return strings.Join(out, " | ")
}

// allowed checks a full argv against the limit's prefixes.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (l Limits) allowed(argv []string) error {
	prefixes, err := ParsePrefixes(l.Commands)
	if err != nil {
		return err
	}
	if _, ok := Match(prefixes, argv); ok {
		return nil
	}
	// The suggestion names the COMMAND, not the whole argv: the arguments here
	// were rendered from templates and may contain a value that belongs to
	// this run rather than to the policy. A longer prefix is how an operator
	// narrows it -- "sudo journalctl" rather than "sudo".
	if len(prefixes) == 0 {
		return fmt.Errorf("no commands are permitted here; add one with "+
			"`ahub config set --allow-cmd %s` (or a longer prefix to narrow it)", argv[0])
	}
	return fmt.Errorf("%q is not permitted here. Permitted: %s. Add it with "+
		"`ahub config set --allow-cmd %s` (or a longer prefix, like %q)",
		strings.Join(argv, " "), Names(prefixes), argv[0], strings.Join(argv[:min(2, len(argv))], " "))
}

// Limits are the local policy a flow runs under.
//
// Both allowlists live in the operator's profile, never in the flow
// definition. A flow is data -- data that a model may have written -- so a
// flow able to name its own executables or hosts would be arbitrary code
// execution with extra steps.
type Limits struct {
	Commands []string
	Hosts    []string
	Timeout  time.Duration
}

func (l Limits) timeout() time.Duration {
	if l.Timeout <= 0 {
		return 60 * time.Second
	}
	return l.Timeout
}

// Output is what a cmd or http step produced.
type Output struct {
	Text   string
	Code   int    // exit status, or HTTP status
	Err    string // stderr, or an error line
	Parsed any    // set when parse: json succeeded
}

// RunCmd executes an allowlisted command.
//
// The command is looked up and run by argv: there is no shell, so a value
// that arrived from a collected input or an earlier step cannot become a
// pipeline, a redirect, or a second command. A non-zero exit is not an error
// here -- it is data. Every tool in this ecosystem follows cli-output-spec,
// so an exit code is a typed answer the flow can branch on: 105 means retry,
// 82 means the arguments were wrong, 92 means it is not there.
func RunCmd(ctx context.Context, lim Limits, command string, args []string, stdin string) (*Output, error) {
	return RunCmdIn(ctx, lim, command, args, stdin, "")
}

// RunCmdIn runs a command in a given directory.
func RunCmdIn(ctx context.Context, lim Limits, command string, args []string, stdin, dir string) (*Output, error) {
	if err := lim.allowed(append([]string{command}, args...)); err != nil {
		return nil, err
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return nil, fmt.Errorf("command %q is allowed but not installed: %w", command, err)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, lim.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		// Which deadline fired matters: naming the step's budget when the
		// flow's had already passed sends a reader to tune the wrong number.
		return nil, deadline(parent, command, lim.timeout())
	}
	out := &Output{
		Text: truncate(stdout.String()),
		Err:  truncate(stderr.String()),
	}
	if cmd.ProcessState != nil {
		out.Code = cmd.ProcessState.ExitCode()
	} else if err != nil {
		return nil, err
	}
	return out, nil
}

// RunCmdTo runs an allowlisted command, streaming both streams to w and
// returning the exit code. It exists for background jobs, whose output can be
// far larger than anything worth holding in memory -- and whose consumer
// reads a tail from the file rather than a string from a variable.
// deadline says which clock ran out.
func deadline(parent context.Context, command string, step time.Duration) error {
	if parent.Err() != nil {
		return fmt.Errorf("command %q was stopped because the run's deadline passed: %v", command, parent.Err())
	}
	return fmt.Errorf("command %q timed out after %s", command, step)
}

func RunCmdTo(ctx context.Context, lim Limits, command string, args []string, w io.Writer) (int, error) {
	if err := lim.allowed(append([]string{command}, args...)); err != nil {
		return 0, err
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return 0, fmt.Errorf("command %q is allowed but not installed: %w", command, err)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, lim.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout, cmd.Stderr = w, w
	cmd.Stdin = nil
	err = cmd.Run()
	if ctx.Err() != nil {
		return 0, deadline(parent, command, lim.timeout())
	}
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode(), nil
	}
	return 0, err
}

// RunHTTP performs one request against an allowlisted host.
func RunHTTP(ctx context.Context, lim Limits, method, rawURL string, headers map[string]string, body string) (*Output, error) {
	if method == "" {
		method = http.MethodGet
	}
	host := hostOf(rawURL)
	if host == "" {
		return nil, fmt.Errorf("cannot determine the host of %q", rawURL)
	}
	if !contains(lim.Hosts, host) {
		return nil, fmt.Errorf("host %q is not allowed here; add it with "+
			"`ahub config set --allow-host %s`", host, host)
	}
	ctx, cancel := context.WithTimeout(ctx, lim.timeout())
	defer cancel()

	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{
		Timeout: lim.timeout(),
		// A redirect is a second request, and it must clear the same bar as
		// the first: an allowlisted host that redirects to an internal
		// address is the ordinary way this kind of step becomes an SSRF.
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if !contains(lim.Hosts, r.URL.Hostname()) {
				return fmt.Errorf("redirect to %q, which is not allowed", r.URL.Hostname())
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOut))
	if err != nil {
		return nil, err
	}
	out := &Output{Text: string(raw), Code: resp.StatusCode}
	if resp.StatusCode >= 400 {
		out.Err = resp.Status
	}
	return out, nil
}

// ParseOutput fills Parsed when the step asked for JSON. A body that does not
// parse is reported rather than silently kept as text: a flow that branches on
// a field would otherwise take the else branch forever and look like it works.
func ParseOutput(o *Output, mode string) error {
	if mode != "json" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(o.Text), &v); err != nil {
		return fmt.Errorf("output was not JSON: %w", err)
	}
	o.Parsed = v
	return nil
}

// Field reads a dotted path out of parsed JSON, so a flow can branch on one
// value without an expression language: "prs.0.number".
func Field(v any, path string) (string, bool) {
	cur := v
	for _, seg := range strings.Split(path, ".") {
		switch t := cur.(type) {
		case map[string]any:
			next, ok := t[seg]
			if !ok {
				return "", false
			}
			cur = next
		case []any:
			i, err := index(seg)
			if err != nil || i < 0 || i >= len(t) {
				return "", false
			}
			cur = t[i]
		default:
			return "", false
		}
	}
	return Scalar(cur), true
}

// Scalar renders a JSON value as the string a flow variable holds. Objects
// and arrays keep their JSON form, so a later step can pass them on intact.
func Scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.6f", t), "0"), ".")
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

func index(s string) (int, error) {
	var i int
	_, err := fmt.Sscanf(s, "%d", &i)
	return i, err
}

func truncate(s string) string {
	if len(s) <= maxOut {
		return s
	}
	return s[:maxOut] + "\n[...truncated]"
}

// LoopbackHosts are refused even when allowlisted, unless the operator opted
// in. Reaching a local service from a flow is legitimate (that is where bkn
// and the lab daemons live) but it should be a decision, not a default.
func IsLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
