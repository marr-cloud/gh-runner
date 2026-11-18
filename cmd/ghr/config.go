package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Config is the fully resolved runner configuration. It is built once from
// the environment by LoadConfig and never mutated afterwards.
type Config struct {
	Scope     string // repo | org | enterprise
	ServerURL string // value for config.sh --url
	APIBase   string // e.g. https://api.github.com
	TokenPath string // runner API path below APIBase, e.g. /repos/o/r/actions/runners
	Owner     string // repo scope only
	Repo      string // repo scope only
	Org       string // org scope only

	// Credentials. Exactly one of: AccessToken, RunnerToken, or AppID+AppKey.
	AccessToken string
	RunnerToken string
	AppID       string
	AppKey      []byte

	Name    string
	Workdir string
	Labels  string
	Group   string

	Ephemeral       bool
	DisableUpdate   bool
	NoDefaultLabels bool
	KeepRegistered  bool // skip automatic deregistration on exit

	RunnerDir string   // where the GitHub runner lives
	Command   []string // optional: run this once registered instead of the runner
}

// Env abstracts the process environment so config loading is testable.
type Env struct {
	Get      func(string) string
	ReadFile func(string) ([]byte, error)
	Hostname string
}

// secret reads KEY, or the file named by KEY_FILE (Docker/Compose secrets).
func (e Env) secret(key string) (string, error) {
	if v := e.Get(key); v != "" {
		return v, nil
	}
	if f := e.Get(key + "_FILE"); f != "" {
		b, err := e.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", key, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", nil
}

func (e Env) first(keys ...string) string {
	for _, k := range keys {
		if v := e.Get(k); v != "" {
			return v
		}
	}
	return ""
}

func (e Env) boolean(key string) (bool, error) {
	v := strings.ToLower(strings.TrimSpace(e.Get(key)))
	switch v {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	}
	return false, fmt.Errorf("%s: %q is not a boolean (use true/false)", key, e.Get(key))
}

const (
	defaultName      = "gh-runner"
	defaultWorkRoot  = "/_work"
	defaultRunnerDir = "/actions-runner"
	maxNameLen       = 64
)

// LoadConfig resolves the configuration from the environment. command, when
// non-empty, replaces the default ./run.sh.
func LoadConfig(e Env, command []string) (*Config, error) {
	c := &Config{Command: command}
	var err error

	c.RunnerDir = e.first("GHR_RUNNER_DIR")
	if c.RunnerDir == "" {
		c.RunnerDir = defaultRunnerDir
	}

	// --- credentials -----------------------------------------------------
	if c.AccessToken, err = e.secret("ACCESS_TOKEN"); err != nil {
		return nil, err
	}
	if c.RunnerToken, err = e.secret("RUNNER_TOKEN"); err != nil {
		return nil, err
	}
	key, err := e.secret("APP_PRIVATE_KEY")
	if err != nil {
		return nil, err
	}
	c.AppID = e.Get("APP_ID")
	if key != "" {
		// Allow single-line keys with literal \n (common in env files).
		c.AppKey = []byte(strings.ReplaceAll(key, `\n`, "\n"))
	}
	hasApp := c.AppID != "" || len(c.AppKey) > 0
	switch {
	case hasApp && (c.AppID == "" || len(c.AppKey) == 0):
		return nil, errors.New("APP_ID and APP_PRIVATE_KEY (or APP_PRIVATE_KEY_FILE) must be set together")
	case hasApp && (c.AccessToken != "" || c.RunnerToken != ""):
		return nil, errors.New("GitHub App credentials are mutually exclusive with ACCESS_TOKEN / RUNNER_TOKEN")
	case !hasApp && c.AccessToken == "" && c.RunnerToken == "":
		return nil, errors.New("no credentials: set ACCESS_TOKEN, RUNNER_TOKEN, or APP_ID + APP_PRIVATE_KEY")
	}

	// --- scope and endpoints ---------------------------------------------
	scope := strings.ToLower(e.Get("RUNNER_SCOPE"))
	if scope == "" && strings.EqualFold(e.Get("ORG_RUNNER"), "true") { // legacy
		scope = "org"
	}
	host := normalizeHost(e.Get("GITHUB_HOST"))

	switch {
	case strings.HasPrefix(scope, "org"):
		c.Scope, c.Org = "org", e.Get("ORG_NAME")
		if c.Org == "" {
			return nil, errors.New("ORG_NAME is required for org runners")
		}
		if host == "" {
			host = "github.com"
		}
		c.ServerURL = "https://" + host + "/" + c.Org
		c.TokenPath = "/orgs/" + c.Org + "/actions/runners"
	case strings.HasPrefix(scope, "ent"):
		c.Scope = "enterprise"
		ent := e.Get("ENTERPRISE_NAME")
		if ent == "" {
			return nil, errors.New("ENTERPRISE_NAME is required for enterprise runners")
		}
		if hasApp {
			return nil, errors.New("GitHub App authentication is not supported for enterprise runners; use ACCESS_TOKEN")
		}
		if host == "" {
			host = "github.com"
		}
		c.ServerURL = "https://" + host + "/enterprises/" + ent
		c.TokenPath = "/enterprises/" + ent + "/actions/runners"
	case scope == "" || scope == "repo" || strings.HasPrefix(scope, "repo"):
		c.Scope = "repo"
		raw := e.Get("REPO_URL")
		if raw == "" {
			return nil, errors.New("REPO_URL is required for repo runners")
		}
		var rhost string
		if c.Owner, c.Repo, rhost, err = parseRepoURL(raw); err != nil {
			return nil, err
		}
		if host == "" {
			host = rhost
		}
		c.ServerURL = "https://" + rhost + "/" + c.Owner + "/" + c.Repo
		c.TokenPath = "/repos/" + c.Owner + "/" + c.Repo + "/actions/runners"
	default:
		return nil, fmt.Errorf("RUNNER_SCOPE: %q is not one of repo, org, enterprise", scope)
	}
	c.APIBase = apiBase(host, e.Get("GITHUB_API_HOST"), e.Get("GITHUB_API_PATH"))

	// --- identity --------------------------------------------------------
	if c.Name, err = runnerName(e); err != nil {
		return nil, err
	}
	c.Workdir = e.Get("RUNNER_WORKDIR")
	if c.Workdir == "" {
		root := e.first("RUNNER_WORKDIR_ROOT")
		if root == "" {
			root = defaultWorkRoot
		}
		c.Workdir = strings.TrimRight(root, "/") + "/" + c.Name
	}
	c.Labels = e.first("RUNNER_LABELS", "LABELS")
	c.Group = e.first("RUNNER_GROUP")

	// --- behaviour -------------------------------------------------------
	for key, dst := range map[string]*bool{
		"EPHEMERAL":                        &c.Ephemeral,
		"DISABLE_AUTO_UPDATE":              &c.DisableUpdate,
		"NO_DEFAULT_LABELS":                &c.NoDefaultLabels,
		"DISABLE_AUTOMATIC_DEREGISTRATION": &c.KeepRegistered,
	} {
		if *dst, err = e.boolean(key); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// runnerName builds "<prefix>-<suffix>". The default suffix is the container
// hostname, which is unique per replica: this is what makes
// `docker compose up --scale` work with no extra state.
func runnerName(e Env) (string, error) {
	prefix := e.first("RUNNER_NAME", "RUNNER_NAME_PREFIX")
	if prefix == "" {
		prefix = defaultName
	}
	mode := strings.ToLower(e.Get("RUNNER_NAME_SUFFIX"))
	if mode == "" {
		switch strings.ToLower(e.Get("RANDOM_RUNNER_SUFFIX")) { // legacy alias
		case "true":
			mode = "random"
		default:
			mode = "hostname"
		}
	}
	var suffix string
	switch mode {
	case "none":
	case "random":
		suffix = randomHex(4)
	case "hostname":
		suffix = e.Hostname
		if suffix == "" {
			suffix = randomHex(4)
		}
	default:
		return "", fmt.Errorf("RUNNER_NAME_SUFFIX: %q is not one of hostname, random, none", mode)
	}
	name := prefix
	if suffix != "" {
		if over := len(prefix) + 1 + len(suffix) - maxNameLen; over > 0 {
			prefix = prefix[:max(0, len(prefix)-over)]
		}
		name = prefix + "-" + suffix
	}
	if len(name) > maxNameLen {
		name = name[:maxNameLen]
	}
	return name, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing is unrecoverable
	}
	return hex.EncodeToString(b)
}

func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	return strings.TrimRight(h, "/")
}

// apiBase mirrors GitHub's own layout: api.github.com for github.com, and
// https://HOST/api/v3 for GitHub Enterprise Server unless overridden.
func apiBase(host, apiHost, apiPath string) string {
	apiPath = strings.TrimSpace(apiPath)
	norm := func(p string) string {
		p = strings.Trim(p, "/")
		if p == "" {
			return ""
		}
		return "/" + p
	}
	switch {
	case apiHost != "":
		if apiPath == "" {
			apiPath = "/api/v3"
		}
		return "https://" + normalizeHost(apiHost) + norm(apiPath)
	case host == "github.com" || host == "":
		return "https://api.github.com" + norm(apiPath)
	default:
		if apiPath == "" {
			apiPath = "/api/v3"
		}
		return "https://" + host + norm(apiPath)
	}
}

func parseRepoURL(raw string) (owner, repo, host string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", "", "", fmt.Errorf("REPO_URL: %q is not a valid URL", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("REPO_URL: %q must look like https://github.com/OWNER/REPO", raw)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), u.Host, nil
}
