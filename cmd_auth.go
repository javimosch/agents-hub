package main

import (
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
	if email == "" || password == "" {
		out.Fail(out.ValidationError, "validation_error",
			"--email and --password are required")
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
		changed := applyConfigSet(fl, &p)
		if len(changed) == 0 {
			out.Fail(out.ValidationError, "validation_error",
				"give at least one of --bkn, --provider, --provider-url, --model, --provider-key, --timeout")
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

func applyConfigSet(fl *flags, p *Config) []string {
	changed := []string{}
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
	if n := fl.intVal("provider-timeout", 0); n > 0 {
		p.ProviderTimeoutSecs = n
		changed = append(changed, "provider_timeout_secs")
	}
	return changed
}

func profileNames(f File) []string {
	names := make([]string, 0, len(f.Profiles))
	for n := range f.Profiles {
		names = append(names, n)
	}
	return names
}
