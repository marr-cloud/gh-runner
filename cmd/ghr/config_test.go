package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEnv(vars map[string]string) Env {
	return Env{
		Get:      func(k string) string { return vars[k] },
		ReadFile: os.ReadFile,
		Hostname: "abc123def456",
	}
}

func TestConfigOrgDefaults(t *testing.T) {
	c, err := LoadConfig(testEnv(map[string]string{
		"RUNNER_SCOPE": "org", "ORG_NAME": "octo", "ACCESS_TOKEN": "pat",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ServerURL": "https://github.com/octo",
		"APIBase":   "https://api.github.com",
		"TokenPath": "/orgs/octo/actions/runners",
		"Name":      "gh-runner-abc123def456",
		"Workdir":   "/_work/gh-runner-abc123def456",
	}
	got := map[string]string{
		"ServerURL": c.ServerURL, "APIBase": c.APIBase, "TokenPath": c.TokenPath,
		"Name": c.Name, "Workdir": c.Workdir,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	if len(c.Command) != 0 {
		t.Errorf("Command = %v, want empty (run the listener)", c.Command)
	}
}

func TestConfigRepoScope(t *testing.T) {
	c, err := LoadConfig(testEnv(map[string]string{
		"REPO_URL": "https://github.com/octo/hello.git", "ACCESS_TOKEN": "pat",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ServerURL != "https://github.com/octo/hello" || c.TokenPath != "/repos/octo/hello/actions/runners" {
		t.Errorf("got %q %q", c.ServerURL, c.TokenPath)
	}
}

func TestConfigEnterpriseAndGHES(t *testing.T) {
	c, err := LoadConfig(testEnv(map[string]string{
		"RUNNER_SCOPE": "enterprise", "ENTERPRISE_NAME": "acme", "ACCESS_TOKEN": "pat",
		"GITHUB_HOST": "https://ghe.example.com/",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ServerURL != "https://ghe.example.com/enterprises/acme" || c.APIBase != "https://ghe.example.com/api/v3" {
		t.Errorf("got %q %q", c.ServerURL, c.APIBase)
	}
}

func TestAPIBase(t *testing.T) {
	cases := []struct{ host, apiHost, apiPath, want string }{
		{"github.com", "", "", "https://api.github.com"},
		{"ghe.example.com", "", "", "https://ghe.example.com/api/v3"},
		{"ghe.example.com", "api.ghe.example.com", "", "https://api.ghe.example.com/api/v3"},
		{"github.com", "", "/custom/", "https://api.github.com/custom"},
		{"ghe.example.com", "", "/", "https://ghe.example.com"},
	}
	for _, tc := range cases {
		if got := apiBase(tc.host, tc.apiHost, tc.apiPath); got != tc.want {
			t.Errorf("apiBase(%q,%q,%q) = %q, want %q", tc.host, tc.apiHost, tc.apiPath, got, tc.want)
		}
	}
}

func TestRunnerName(t *testing.T) {
	base := map[string]string{"RUNNER_SCOPE": "org", "ORG_NAME": "o", "ACCESS_TOKEN": "t"}
	with := func(extra map[string]string) *Config {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		c, err := LoadConfig(testEnv(m), nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if n := with(map[string]string{"RUNNER_NAME": "ci"}).Name; n != "ci-abc123def456" {
		t.Errorf("prefix+hostname: %q", n)
	}
	if n := with(map[string]string{"RUNNER_NAME": "ci", "RUNNER_NAME_SUFFIX": "none"}).Name; n != "ci" {
		t.Errorf("none: %q", n)
	}
	n := with(map[string]string{"RUNNER_NAME_SUFFIX": "random"}).Name
	if !strings.HasPrefix(n, "gh-runner-") || n == "gh-runner-abc123def456" || len(n) != len("gh-runner-")+8 {
		t.Errorf("random: %q", n)
	}
	if n := with(map[string]string{"RANDOM_RUNNER_SUFFIX": "true"}).Name; n == "gh-runner-abc123def456" {
		t.Errorf("legacy RANDOM_RUNNER_SUFFIX ignored: %q", n)
	}
	long := with(map[string]string{"RUNNER_NAME": strings.Repeat("x", 80)}).Name
	if len(long) != maxNameLen || !strings.HasSuffix(long, "-abc123def456") {
		t.Errorf("long name not truncated correctly: %q (%d)", long, len(long))
	}
	// Explicit workdir wins over the per-name default.
	if w := with(map[string]string{"RUNNER_WORKDIR": "/w"}).Workdir; w != "/w" {
		t.Errorf("workdir: %q", w)
	}
	if w := with(map[string]string{"RUNNER_WORKDIR_ROOT": "/tmp/runners/"}).Workdir; w != "/tmp/runners/gh-runner-abc123def456" {
		t.Errorf("workdir root: %q", w)
	}
}

func TestConfigErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"no credentials":     {"REPO_URL": "https://github.com/o/r"},
		"repo needs url":     {"ACCESS_TOKEN": "t"},
		"org needs name":     {"RUNNER_SCOPE": "org", "ACCESS_TOKEN": "t"},
		"bad scope":          {"RUNNER_SCOPE": "galaxy", "ACCESS_TOKEN": "t"},
		"bad repo url":       {"REPO_URL": "https://github.com/onlyowner", "ACCESS_TOKEN": "t"},
		"bad bool":           {"REPO_URL": "https://github.com/o/r", "ACCESS_TOKEN": "t", "EPHEMERAL": "maybe"},
		"bad suffix":         {"REPO_URL": "https://github.com/o/r", "ACCESS_TOKEN": "t", "RUNNER_NAME_SUFFIX": "x"},
		"app incomplete":     {"REPO_URL": "https://github.com/o/r", "APP_ID": "1"},
		"app + token":        {"REPO_URL": "https://github.com/o/r", "APP_ID": "1", "APP_PRIVATE_KEY": "k", "ACCESS_TOKEN": "t"},
		"enterprise + app":   {"RUNNER_SCOPE": "enterprise", "ENTERPRISE_NAME": "e", "APP_ID": "1", "APP_PRIVATE_KEY": "k"},
		"missing token file": {"REPO_URL": "https://github.com/o/r", "ACCESS_TOKEN_FILE": "/nonexistent/x"},
	}
	for name, vars := range cases {
		if _, err := LoadConfig(testEnv(vars), nil); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSecretFromFileAndBools(t *testing.T) {
	f := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(f, []byte("  filetoken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(testEnv(map[string]string{
		"REPO_URL": "https://github.com/o/r", "ACCESS_TOKEN_FILE": f,
		"EPHEMERAL": "true", "DISABLE_AUTO_UPDATE": "1", "NO_DEFAULT_LABELS": "false",
	}), []string{"echo", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessToken != "filetoken" || !c.Ephemeral || !c.DisableUpdate || c.NoDefaultLabels {
		t.Errorf("unexpected config: %+v", c)
	}
	if strings.Join(c.Command, " ") != "echo hi" {
		t.Errorf("command: %v", c.Command)
	}
}

func TestDescribeHasNoSecrets(t *testing.T) {
	c, err := LoadConfig(testEnv(map[string]string{
		"REPO_URL": "https://github.com/o/r", "ACCESS_TOKEN": "supersecret",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.describe(), "supersecret") {
		t.Error("describe() leaks the token")
	}
}

func TestChildEnvScrubsSecrets(t *testing.T) {
	in := []string{
		"PATH=/bin", "ACCESS_TOKEN=a", "RUNNER_TOKEN=b", "APP_ID=1", "APP_LOGIN=x",
		"APP_PRIVATE_KEY=k", "ACCESS_TOKEN_FILE=/s", "APP_PRIVATE_KEY_FILE=/k", "KEEP=1",
	}
	out := strings.Join(childEnv(in, 1000), "\n")
	for _, bad := range []string{"ACCESS_TOKEN", "RUNNER_TOKEN", "APP_ID", "APP_LOGIN", "APP_PRIVATE_KEY"} {
		if strings.Contains(out, bad) {
			t.Errorf("%s survived scrubbing", bad)
		}
	}
	if !strings.Contains(out, "PATH=/bin") || !strings.Contains(out, "KEEP=1") {
		t.Error("non-secret variables were dropped")
	}
	if strings.Contains(out, "RUNNER_ALLOW_RUNASROOT") {
		t.Error("RUNASROOT set for non-root")
	}
	if !strings.Contains(strings.Join(childEnv(in, 0), "\n"), "RUNNER_ALLOW_RUNASROOT=1") {
		t.Error("RUNASROOT missing for root")
	}
}

func TestRealMainArgs(t *testing.T) {
	t.Setenv("REPO_URL", "https://github.com/o/r")
	t.Setenv("ACCESS_TOKEN", "x")
	if code := realMain([]string{"--dry-run"}); code != 0 {
		t.Errorf("--dry-run: %d", code)
	}
	if code := realMain([]string{"--version"}); code != 0 {
		t.Errorf("--version: %d", code)
	}
	if code := realMain([]string{"--bogus"}); code != 2 {
		t.Errorf("unknown flag: %d, want 2", code)
	}
}
