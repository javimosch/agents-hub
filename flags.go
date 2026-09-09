package main

import (
	"strconv"
	"strings"
)

// flags are the global options, accepted anywhere on the line -- before or
// after the subcommand -- because an agent composing a command from a template
// should not have to know where a flag is allowed to sit.
type flags struct {
	profile     string
	bknURL      string
	token       string
	provider    string
	providerURL string
	model       string
	human       bool
	stream      bool
	rest        []string
	kv          map[string]string
	// multi keeps every occurrence of a repeated flag. kv keeps the last, so
	// a command that wants one value is unaffected by a caller that passed
	// several, and a command that wants them all can have them.
	multi map[string][]string
	bools map[string]bool
}

func newFlags() *flags {
	return &flags{kv: map[string]string{}, multi: map[string][]string{}, bools: map[string]bool{}}
}

// parse splits argv into global flags, command-specific key/values, boolean
// switches and positional arguments. It accepts --k=v and --k v alike.
func parse(argv []string) *flags {
	f := newFlags()
	valued := map[string]*string{
		"profile": &f.profile, "server": &f.bknURL, "bkn": &f.bknURL,
		"token": &f.token, "provider": &f.provider,
		"provider-url": &f.providerURL, "model": &f.model,
	}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "--") {
			f.rest = append(f.rest, a)
			continue
		}
		name := strings.TrimPrefix(a, "--")
		val := ""
		hasVal := false
		if k, v, ok := strings.Cut(name, "="); ok {
			name, val, hasVal = k, v, true
		}
		switch name {
		case "human":
			f.human = true
			continue
		case "stream":
			f.stream = true
			continue
		}
		if !hasVal {
			// A flag whose next token starts with -- is a boolean switch;
			// otherwise it consumes that token as its value.
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "--") {
				val, hasVal = argv[i+1], true
				i++
			}
		}
		if !hasVal {
			f.bools[name] = true
			continue
		}
		if p, ok := valued[name]; ok {
			*p = val
			continue
		}
		f.kv[name] = val
		f.multi[name] = append(f.multi[name], val)
	}
	return f
}

// list returns every value given for a repeated flag.
func (f *flags) list(k string) []string { return f.multi[k] }

func (f *flags) arg(i int) string {
	if i < len(f.rest) {
		return f.rest[i]
	}
	return ""
}

func (f *flags) str(k string) string { return f.kv[k] }

func (f *flags) has(k string) bool {
	_, ok := f.kv[k]
	return ok || f.bools[k]
}

func (f *flags) intVal(k string, def int) int {
	if v, ok := f.kv[k]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func (f *flags) floatPtr(k string) (*float64, bool) {
	v, ok := f.kv[k]
	if !ok {
		return nil, true
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil, false
	}
	return &n, true
}
