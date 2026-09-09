package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config is everything ahub needs to reach its two dependencies: a bkn
// instance for state and identity, and a model provider for answers.
//
// Resolution order is flags > env > profile > default, and it is deliberately
// the same order another CLI uses -- an agent that has learned one of these tools
// should not have to relearn where the token comes from.
type Config struct {
	BknURL      string `json:"bkn_url,omitempty"`
	Token       string `json:"token,omitempty"`
	Refresh     string `json:"refresh,omitempty"`
	Provider    string `json:"provider,omitempty"`
	ProviderURL string `json:"provider_url,omitempty"`
	ProviderKey string `json:"provider_key,omitempty"`
	Model       string `json:"model,omitempty"`
	TimeoutSecs int    `json:"timeout_secs,omitempty"`
	// ProviderTimeoutSecs is separate on purpose. A bkn call is a database
	// read over the network and a minute is generous; a model call with a
	// large prompt and a reasoning budget can legitimately run for minutes,
	// and one shared number means either bkn hangs too long or generation is
	// cut off mid-answer.
	ProviderTimeoutSecs int `json:"provider_timeout_secs,omitempty"`
	// JobTimeoutSecs bounds a background job. It is generous because a job
	// exists precisely for work that does not fit in a turn, but it is not
	// unbounded: a wedged process nobody reaps is worse than a failed one.
	JobTimeoutSecs int `json:"job_timeout_secs,omitempty"`
	// ModelLimits is what each model's context window is believed to be.
	// Entries are learned from a provider's refusal or set by hand; a model
	// that is not listed uses a deliberately conservative default.
	ModelLimits map[string]int `json:"model_limits,omitempty"`
	// CompactAt is the fraction of the window at which a thread is folded
	// down. Zero means the default.
	CompactAt float64 `json:"compact_at,omitempty"`

	// AllowCmds and AllowHosts are what a flow step may reach on this
	// machine. They live here, in a file the operator owns, and never in a
	// flow definition: a flow is data -- possibly data a model wrote -- and a
	// flow that could name its own executables would be arbitrary code
	// execution with extra steps.
	AllowCmds  []string `json:"allow_cmds,omitempty"`
	AllowHosts []string `json:"allow_hosts,omitempty"`
	// AllowToolCmds is a SEPARATE list, and separate on purpose. A flow step
	// runs argv a human wrote; the cmd tool runs argv a model wrote. So this
	// list holds command PREFIXES ("gh pr list") rather than bare names: the
	// model may append arguments, never change the prefix. Allowlisting bare
	// "git" for a model would permit `git push --force`.
	AllowToolCmds []string `json:"allow_tool_cmds,omitempty"`
	// Secrets are referenced from a flow as {{secret.NAME}} and never stored
	// in one, so a definition can be shared without its credentials.
	Secrets map[string]string `json:"secrets,omitempty"`
}

// File is the on-disk shape: named profiles plus which one is active.
type File struct {
	ActiveProfile string            `json:"activeProfile,omitempty"`
	Profiles      map[string]Config `json:"profiles,omitempty"`
}

const (
	defaultBkn      = "http://127.0.0.1:7799"
	defaultProvider = "openai"
	defaultProfile  = "default"
)

// configPath is where a profile is written. It follows XDG so a container can
// relocate the whole thing with one variable.
func configPath() string {
	if p := os.Getenv("AHUB_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "agents-hub.json"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "agents-hub", "config.json")
}

func loadFile() (File, error) {
	var f File
	raw, err := os.ReadFile(configPath())
	if os.IsNotExist(err) {
		return File{Profiles: map[string]Config{}}, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("%s is not valid JSON: %w", configPath(), err)
	}
	if f.Profiles == nil {
		f.Profiles = map[string]Config{}
	}
	return f, nil
}

func saveFile(f File) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	// 0600: this file holds a bearer token and a provider API key.
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

func profileName(fl *flags, f File) string {
	if fl.profile != "" {
		return fl.profile
	}
	if v := os.Getenv("AHUB_PROFILE"); v != "" {
		return v
	}
	if f.ActiveProfile != "" {
		return f.ActiveProfile
	}
	return defaultProfile
}

// resolve merges the three sources into one config.
func resolve(fl *flags) (Config, string, error) {
	f, err := loadFile()
	if err != nil {
		return Config{}, "", err
	}
	name := profileName(fl, f)
	c := f.Profiles[name]

	pick := func(flagV, envK, cur, def string) string {
		if flagV != "" {
			return flagV
		}
		if v := os.Getenv(envK); v != "" {
			return v
		}
		if cur != "" {
			return cur
		}
		return def
	}
	c.BknURL = pick(fl.bknURL, "AHUB_BKN_URL", c.BknURL, defaultBkn)
	c.Token = pick(fl.token, "AHUB_TOKEN", c.Token, "")
	c.Provider = pick(fl.provider, "AHUB_PROVIDER", c.Provider, defaultProvider)
	c.ProviderURL = pick(fl.providerURL, "AHUB_PROVIDER_URL", c.ProviderURL, "")
	c.ProviderKey = pick("", "AHUB_PROVIDER_KEY", c.ProviderKey, "")
	c.Model = pick(fl.model, "AHUB_MODEL", c.Model, "")
	if c.TimeoutSecs == 0 {
		c.TimeoutSecs = 60
	}
	if c.ProviderTimeoutSecs == 0 {
		c.ProviderTimeoutSecs = 300
	}
	if c.JobTimeoutSecs == 0 {
		c.JobTimeoutSecs = 3600
	}
	envInt("AHUB_TIMEOUT", &c.TimeoutSecs)
	envInt("AHUB_PROVIDER_TIMEOUT", &c.ProviderTimeoutSecs)
	envInt("AHUB_JOB_TIMEOUT", &c.JobTimeoutSecs)
	return c, name, nil
}

func envInt(key string, dst *int) {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			*dst = n
		}
	}
}

func (c Config) timeout() time.Duration { return time.Duration(c.TimeoutSecs) * time.Second }

func (c Config) providerTimeout() time.Duration {
	return time.Duration(c.ProviderTimeoutSecs) * time.Second
}
