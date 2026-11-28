package main

import (
	"io"
	"os"
	"slices"
	"strings"

	"github.com/javimosch/agents-hub/internal/flow"
	"github.com/javimosch/agents-hub/internal/out"
)

// maxSecret bounds what is read as one secret value.
const maxSecret = 64 << 10

// readSecretInput adds the value named by --secret-input, read from stdin, to
// a run's inputs. It refuses a secret given by --input, which would sit on the
// command line for ps and the shell history to keep. No message repeats a
// value.
func readSecretInput(fl *flags, f *flow.Flow, inputs map[string]string) {
	secrets := flow.SecretVars(f)
	for name := range inputs {
		if slices.Contains(secrets, name) {
			out.Fail(out.InvalidValue, "secret_on_command_line",
				name+" is a secret input and may not be given with --input; nothing was run. "+
					"Treat the value as exposed",
				secretCommand(name))
		}
	}
	if !fl.has("secret-input") {
		return
	}
	names := fl.list("secret-input")
	if len(names) != 1 || !slices.Contains(secrets, names[0]) {
		// Anything else would be stored with the run.
		out.Fail(out.InvalidValue, "invalid_value",
			"--secret-input takes the name of one secret input of this flow: "+strings.Join(secrets, ", "))
	}
	info, err := os.Stdin.Stat()
	if err != nil || (info.Mode()&os.ModeNamedPipe == 0 && !info.Mode().IsRegular()) {
		// A terminal would wait, and show what is typed.
		out.Fail(out.ValidationError, "validation_error",
			"--secret-input reads the value from a pipe or a file", secretCommand(names[0]))
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, maxSecret+1))
	if err != nil {
		out.Fail(out.ValidationError, "validation_error", "could not read the secret: "+err.Error())
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if value == "" || len(raw) > maxSecret {
		out.Fail(out.ValidationError, "validation_error",
			"the value on stdin is empty or over 64KiB", secretCommand(names[0]))
	}
	inputs[names[0]] = value
}

func secretCommand(name string) string {
	return `printf '%s' "$VALUE" | ahub flow resume <run-id> --secret-input ` + name
}
