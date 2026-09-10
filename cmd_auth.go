package main

import (
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/javimosch/agents-hub/internal/bkn"
	"github.com/javimosch/agents-hub/internal/hub"
	"github.com/javimosch/agents-hub/internal/out"
)

// setupCmd declares the hub's collections and their owner-scoped policy.
//
// It is the only command that touches an admin token, and it takes it as an
// explicit flag rather than reading it from the profile: an admin credential
// that lives in the same config the serving path reads is an admin credential
// that will eventually be used by the serving path.
func setupCmd(fl *flags) {
	cfg, _, err := resolve(fl)
	if err != nil {
		out.Fail(out.ValidationError, "config_error", err.Error())
	}
	admin := fl.str("admin-token")
	if admin == "" {
		out.Fail(out.ValidationError, "validation_error",
			"--admin-token is required",
			"ahub setup --admin-token \"$(cat /opt/bkn/home/admin.tok)\"")
	}
	c := bkn.New(cfg.BknURL, admin, cfg.timeout())
	if err := c.Health(); err != nil {
		out.Fail(out.ConnectionError, "connection_error", err.Error(),
			"check --bkn "+cfg.BknURL)
	}
	declared, err := hub.Setup(c)
	if err != nil {
		out.Log("declared %d of %d collections before failing", len(declared), len(hub.Refs()))
		fail(err, "declare")
	}
	out.Data(map[string]any{
		"bkn":         cfg.BknURL,
		"collections": declared,
		"policy":      "owner-scoped on " + hub.OwnerFieldName,
	})
}

// loginCmd exchanges credentials for tokens and writes them to the profile.
func loginCmd(fl *flags) {
	cfg, name, err := resolve(fl)
	if err != nil {
		out.Fail(out.ValidationError, "config_error", err.Error())
	}
	email, password := fl.str("email"), fl.str("password")
	if fl.has("password-stdin") {
		// Read from stdin so the credential never appears in an argument
		// list, where anything on the machine can read it out of /proc.
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			out.Fail(out.ValidationError, "validation_error", "could not read the password from stdin")
		}
		password = strings.TrimRight(string(raw), "\r\n")
	}
	if email == "" || password == "" {
		out.Fail(out.ValidationError, "validation_error",
			"--email and a password are required",
			"ahub login --email you@example.com --password-stdin  < a file, or a pipe")
	}
	c := bkn.New(cfg.BknURL, "", cfg.timeout())
	tokens, err := c.Login(email, password, fl.str("org"))
	if err != nil {
		fail(err, "login")
	}
	f, err := loadFile()
	if err != nil {
		out.Fail(out.ValidationError, "config_error", err.Error())
	}
	p := f.Profiles[name]
	p.BknURL, p.Token, p.Refresh = cfg.BknURL, tokens.Access, tokens.Refresh
	if f.Profiles == nil {
		f.Profiles = map[string]Config{}
	}
	f.Profiles[name] = p
	if f.ActiveProfile == "" {
		f.ActiveProfile = name
	}
	if err := saveFile(f); err != nil {
		out.Fail(out.InternalError, "internal_error", err.Error())
	}
	out.Log("token stored in profile %q at %s", name, configPath())
	out.Data(map[string]any{"profile": name, "bkn": cfg.BknURL, "email": email})
}

// whoamiCmd reports the identity behind the current token. It is the fastest
// way to tell an expired token from a wrong URL.
func whoamiCmd(fl *flags) {
	c, cfg := client(fl)
	me, err := c.Me()
	if err != nil {
		fail(err, "whoami")
	}
	out.Data(map[string]any{"bkn": cfg.BknURL, "me": me})
}

// configCmd shows or edits the profile. Secrets are never printed back: a
// token that reaches stdout reaches every log that captured the command.
func configCmd(fl *flags) {
	f, err := loadFile()
	if err != nil {
		out.Fail(out.ValidationError, "config_error", err.Error())
	}
	switch fl.arg(0) {
	case "", "show":
		cfg, name, err := resolve(fl)
		if err != nil {
			out.Fail(out.ValidationError, "config_error", err.Error())
		}
		out.Data(map[string]any{
			"path": configPath(), "profile": name,
			"profiles": profileNames(f),
			"config": map[string]any{
				"bkn_url": cfg.BknURL, "provider": cfg.Provider,
				"provider_url": cfg.ProviderURL, "model": cfg.Model,
				"timeout_secs": cfg.TimeoutSecs, "provider_timeout_secs": cfg.ProviderTimeoutSecs,
				"token_set": cfg.Token != "", "provider_key_set": cfg.ProviderKey != "",
				"allow_cmds": cfg.AllowCmds, "allow_hosts": cfg.AllowHosts,
				"allow_tool_cmds": cfg.AllowToolCmds,
				"model_limits":    cfg.ModelLimits, "compact_at": cfg.CompactAt,
				"daily_token_budget": cfg.DailyTokenBudget,
				"job_retention_days": cfg.JobRetentionDays,
				// Names only. A secret printed to stdout is a secret in every
				// log that captured the command.
				"secrets": secretNames(cfg),
			},
		})
	case "set-profile":
		name := fl.arg(1)
		if name == "" {
			out.Fail(out.ValidationError, "validation_error", "give a profile name")
		}
		if _, ok := f.Profiles[name]; !ok {
			f.Profiles[name] = Config{}
		}
		f.ActiveProfile = name
		if err := saveFile(f); err != nil {
			out.Fail(out.InternalError, "internal_error", err.Error())
		}
		out.Data(map[string]any{"profile": name, "path": configPath()})
	case "set":
		name := profileName(fl, f)
		p := f.Profiles[name]
		changed, asked := applyConfigSet(fl, &p)
		if len(changed) == 0 && !asked {
			out.Fail(out.ValidationError, "validation_error",
				"give at least one of --bkn, --provider, --provider-url, --model, "+
					"--provider-key, --timeout, --allow-cmd, --allow-host, --allow-tool-cmd, --secret")
		}
		f.Profiles[name] = p
		if f.ActiveProfile == "" {
			f.ActiveProfile = name
		}
		if err := saveFile(f); err != nil {
			out.Fail(out.InternalError, "internal_error", err.Error())
		}
		out.Data(map[string]any{"profile": name, "changed": changed, "path": configPath()})
	default:
		out.Fail(out.InvalidArguments, "unknown_command",
			"config takes show, set, or set-profile")
	}
}

// applyConfigSet returns what changed, and whether anything was asked for at
// all -- the two are different, because asking for a setting that is already
// in place changed nothing and is still a success.
func applyConfigSet(fl *flags, p *Config) (changed []string, asked bool) {
	changed = []string{}
	set := func(name string, val string, dst *string) {
		if val != "" {
			*dst = val
			changed = append(changed, name)
		}
	}
	set("bkn_url", fl.bknURL, &p.BknURL)
	set("provider", fl.provider, &p.Provider)
	set("provider_url", fl.providerURL, &p.ProviderURL)
	set("model", fl.model, &p.Model)
	set("provider_key", fl.str("provider-key"), &p.ProviderKey)
	if n := fl.intVal("timeout", 0); n > 0 {
		p.TimeoutSecs = n
		changed = append(changed, "timeout_secs")
	}
	for _, c := range fl.list("allow-cmd") {
		asked = true
		if !contains(p.AllowCmds, c) {
			p.AllowCmds = append(p.AllowCmds, c)
			changed = append(changed, "allow_cmds")
		}
	}
	for _, hst := range fl.list("allow-host") {
		asked = true
		if !contains(p.AllowHosts, hst) {
			p.AllowHosts = append(p.AllowHosts, hst)
			changed = append(changed, "allow_hosts")
		}
	}
	for _, ml := range fl.list("model-limit") {
		asked = true
		name, val, ok := strings.Cut(ml, "=")
		if !ok {
			out.Fail(out.InvalidValue, "invalid_value", "--model-limit takes model=tokens")
		}
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || n <= 0 {
			out.Fail(out.InvalidValue, "invalid_value", "--model-limit needs a positive token count")
		}
		if p.ModelLimits == nil {
			p.ModelLimits = map[string]int{}
		}
		p.ModelLimits[strings.TrimSpace(name)] = n
		changed = append(changed, "model_limits")
	}
	if v := fl.str("compact-at"); v != "" {
		asked = true
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 || f >= 1 {
			out.Fail(out.InvalidValue, "invalid_value",
				"--compact-at takes a fraction between 0 and 1, like 0.6")
		}
		p.CompactAt = f
		changed = append(changed, "compact_at")
	}
	for _, s := range fl.list("secret") {
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			out.Fail(out.InvalidValue, "invalid_value", "--secret takes NAME=value")
		}
		if p.Secrets == nil {
			p.Secrets = map[string]string{}
		}
		p.Secrets[k] = v
		changed = append(changed, "secrets")
	}
	for _, c := range fl.list("allow-tool-cmd") {
		// Re-adding something already permitted is a no-op that SUCCEEDS.
		// Setup is meant to be re-runnable, and an agent repeating a step it
		// already did must never see a false failure.
		asked = true
		if !contains(p.AllowToolCmds, c) {
			p.AllowToolCmds = append(p.AllowToolCmds, c)
			changed = append(changed, "allow_tool_cmds")
		}
	}
	for _, c := range fl.list("deny-tool-cmd") {
		p.AllowToolCmds = without(p.AllowToolCmds, c)
		changed = append(changed, "allow_tool_cmds")
	}
	for _, c := range fl.list("deny-cmd") {
		p.AllowCmds = without(p.AllowCmds, c)
		changed = append(changed, "allow_cmds")
	}
	for _, hst := range fl.list("deny-host") {
		p.AllowHosts = without(p.AllowHosts, hst)
		changed = append(changed, "allow_hosts")
	}
	if n := fl.intVal("daily-token-budget", 0); n > 0 {
		p.DailyTokenBudget = n
		changed = append(changed, "daily_token_budget")
	}
	if n := fl.intVal("job-retention-days", 0); n > 0 {
		p.JobRetentionDays = n
		changed = append(changed, "job_retention_days")
	}
	if n := fl.intVal("provider-timeout", 0); n > 0 {
		p.ProviderTimeoutSecs = n
		changed = append(changed, "provider_timeout_secs")
	}
	return changed, asked
}

// secretNames lists which secrets exist without revealing any of them: a
// value printed to stdout is a value in every log that captured the command.
func secretNames(c Config) []string {
	names := make([]string, 0, len(c.Secrets))
	for n := range c.Secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func without(list []string, s string) []string {
	out := list[:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

func profileNames(f File) []string {
	names := make([]string, 0, len(f.Profiles))
	for n := range f.Profiles {
		names = append(names, n)
	}
	return names
}
