package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeRunner lays out a directory that behaves like an extracted
// actions/runner for the parts ghr touches: bin/Runner.Listener with the
// configure / remove / run verbs. runBody is the shell for `run`.
func fakeRunner(t *testing.T, runBody string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1" in
configure) echo "$@" >> "$FAKE_LOG/calls.log"; touch .runner ;;
remove)    echo "$@" >> "$FAKE_LOG/calls.log"; rm -f .runner ;;
run)
` + runBody + `
;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "bin", "Runner.Listener"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func setListener(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "bin", "Runner.Listener"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newSupervisor(t *testing.T, dir string, cfg *Config, srv *httptest.Server, logDir string) *Supervisor {
	t.Helper()
	cfg.RunnerDir = dir
	cfg.APIBase = srv.URL
	if cfg.Workdir == "" {
		cfg.Workdir = filepath.Join(t.TempDir(), "work")
	}
	client := NewClient(cfg)
	client.backoff = time.Millisecond
	env := append(childEnv(os.Environ(), os.Getuid()), "FAKE_LOG="+logDir)
	return &Supervisor{
		cfg: cfg, api: client, environ: env,
		log:        slog.New(slog.NewTextHandler(os.Stderr, nil)),
		retryDelay: 10 * time.Millisecond,
		updateWait: 2 * time.Second,
	}
}

func apiServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/registration-token"):
			_, _ = w.Write([]byte(`{"token":"REG"}`))
		case strings.HasSuffix(r.URL.Path, "/remove-token"):
			_, _ = w.Write([]byte(`{"token":"RM"}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func baseCfg() *Config {
	return &Config{
		Scope: "org", Org: "octo", AccessToken: "pat", ServerURL: "https://github.com/octo",
		TokenPath: "/orgs/octo/actions/runners", Name: "r-1", Labels: "a,b", Group: "G",
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func callLines(t *testing.T, logDir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(logDir, "calls.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func runAsync(s *Supervisor, sigs chan os.Signal) chan int {
	ch := make(chan int, 1)
	go func() { ch <- s.Run(context.Background(), sigs) }()
	return ch
}

func waitFor(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func TestLifecycleSignalAndDeregister(t *testing.T) {
	logDir := t.TempDir()
	// `run` records its env, signals readiness, then waits for SIGTERM.
	dir := fakeRunner(t, `
env > "$FAKE_LOG/run.env"
touch "$FAKE_LOG/ready"
trap 'exit 0' TERM INT
while true; do sleep 0.05; done`)
	srv := apiServer(t)
	defer srv.Close()
	cfg := baseCfg()
	cfg.Ephemeral = true
	s := newSupervisor(t, dir, cfg, srv, logDir)
	// Pretend secrets were in our env: childEnv must have stripped them.
	s.environ = append(childEnv([]string{"PATH=" + os.Getenv("PATH"), "ACCESS_TOKEN=leak", "APP_PRIVATE_KEY=leak"}, 1000), "FAKE_LOG="+logDir)

	sigs := make(chan os.Signal, 1)
	codeCh := runAsync(s, sigs)
	waitFor(t, filepath.Join(logDir, "ready"))
	sigs <- syscall.SIGTERM

	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not exit after SIGTERM")
	}

	lines := callLines(t, logDir)
	if len(lines) != 2 {
		t.Fatalf("listener calls = %q", lines)
	}
	for _, want := range []string{
		"configure", "--unattended", "--replace", "--url https://github.com/octo", "--token REG",
		"--name r-1", "--labels a,b", "--runnergroup G", "--ephemeral",
	} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("configure args missing %q: %s", want, lines[0])
		}
	}
	if strings.Contains(lines[0], "--disableupdate") || strings.Contains(lines[0], "--no-default-labels") {
		t.Errorf("unexpected flags: %s", lines[0])
	}
	if lines[1] != "remove --token RM" {
		t.Errorf("deregister call = %q", lines[1])
	}
	if strings.Contains(readFile(t, filepath.Join(logDir, "run.env")), "leak") {
		t.Error("secrets reached the runner process environment")
	}
	if _, err := os.Stat(filepath.Join(dir, ".runner")); err == nil {
		t.Error(".runner still present after deregistration")
	}
}

func TestExitCodePropagatesAndStaleFilesRemoved(t *testing.T) {
	logDir := t.TempDir()
	dir := fakeRunner(t, "exit 7")
	for _, f := range staleFiles {
		_ = os.WriteFile(filepath.Join(dir, f), []byte("old"), 0o600)
	}
	srv := apiServer(t)
	defer srv.Close()
	s := newSupervisor(t, dir, baseCfg(), srv, logDir)
	if code := s.Run(context.Background(), make(chan os.Signal)); code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	if _, err := os.Stat(filepath.Join(dir, ".credentials")); err == nil {
		t.Error("stale .credentials not removed")
	}
	if n := len(callLines(t, logDir)); n != 2 {
		t.Errorf("expected configure + remove, got %d calls", n)
	}
}

func TestEphemeralSelfRemovedSkipsDeregister(t *testing.T) {
	logDir := t.TempDir()
	// An ephemeral runner deletes its own .runner after the job.
	dir := fakeRunner(t, "rm -f .runner; exit 0")
	srv := apiServer(t)
	defer srv.Close()
	cfg := baseCfg()
	cfg.Ephemeral = true
	s := newSupervisor(t, dir, cfg, srv, logDir)
	if code := s.Run(context.Background(), make(chan os.Signal)); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if n := len(callLines(t, logDir)); n != 1 {
		t.Errorf("expected only the configure call, got %d", n)
	}
}

func TestKeepRegisteredSkipsDeregister(t *testing.T) {
	logDir := t.TempDir()
	dir := fakeRunner(t, "exit 0")
	srv := apiServer(t)
	defer srv.Close()
	cfg := baseCfg()
	cfg.KeepRegistered = true
	s := newSupervisor(t, dir, cfg, srv, logDir)
	if code := s.Run(context.Background(), make(chan os.Signal)); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if n := len(callLines(t, logDir)); n != 1 {
		t.Errorf("expected only the configure call, got %d", n)
	}
}

func TestConfigureFailureReturnsItsExitCode(t *testing.T) {
	logDir := t.TempDir()
	dir := fakeRunner(t, "exit 0")
	setListener(t, dir, "exit 5\n")
	srv := apiServer(t)
	defer srv.Close()
	s := newSupervisor(t, dir, baseCfg(), srv, logDir)
	if code := s.Run(context.Background(), make(chan os.Signal)); code != 5 {
		t.Fatalf("exit code = %d, want 5", code)
	}
}

func TestUnwritableWorkdirExplainsTheFix(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	logDir := t.TempDir()
	dir := fakeRunner(t, "exit 0")
	srv := apiServer(t)
	defer srv.Close()
	root := filepath.Join(t.TempDir(), "runners")
	if err := os.Mkdir(root, 0o555); err != nil { // like a bind mount Docker created as root
		t.Fatal(err)
	}
	cfg := baseCfg()
	cfg.Workdir = filepath.Join(root, "r-1")
	s := newSupervisor(t, dir, cfg, srv, logDir)
	var buf strings.Builder
	s.log = slog.New(slog.NewTextHandler(&buf, nil))

	if code := s.Run(context.Background(), make(chan os.Signal)); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	out := buf.String()
	for _, want := range []string{"uid " + strconv.Itoa(os.Getuid()), "install -d -o " + strconv.Itoa(os.Getuid()), root} {
		if !strings.Contains(out, want) {
			t.Errorf("log should mention %q:\n%s", want, out)
		}
	}
}

func TestTokenFailureAbortsBeforeConfigure(t *testing.T) {
	logDir := t.TempDir()
	dir := fakeRunner(t, "exit 0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	s := newSupervisor(t, dir, baseCfg(), srv, logDir)
	if code := s.Run(context.Background(), make(chan os.Signal)); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(callLines(t, logDir)) != 0 {
		t.Error("the listener must not run without a token")
	}
}

func TestSignalDuringConfigureAborts(t *testing.T) {
	logDir := t.TempDir()
	dir := fakeRunner(t, "exit 0")
	// configure hangs (as if GitHub were slow); SIGTERM must abort promptly.
	setListener(t, dir, "touch \"$FAKE_LOG/configuring\"\nsleep 30\n")
	srv := apiServer(t)
	defer srv.Close()
	s := newSupervisor(t, dir, baseCfg(), srv, logDir)
	sigs := make(chan os.Signal, 1)
	codeCh := runAsync(s, sigs)
	waitFor(t, filepath.Join(logDir, "configuring"))
	sigs <- syscall.SIGTERM
	select {
	case code := <-codeCh:
		if code != exitSignalled {
			t.Fatalf("exit code = %d, want %d", code, exitSignalled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("configure was not interrupted by SIGTERM")
	}
}

// loopRunner returns a runner whose `run` exits with the codes in seq, one
// per launch (tracked in a counter file), then exits 0.
func loopRunner(t *testing.T, logDir string, seq []string, extra string) string {
	t.Helper()
	cases := ""
	for i, c := range seq {
		cases += strings.Replace("		N) "+extra+" exit C ;;\n", "N", string(rune('0'+i)), 1)
		cases = strings.Replace(cases, "C", c, 1)
	}
	body := `
n=$(cat "$FAKE_LOG/n" 2>/dev/null || echo 0)
echo $((n+1)) > "$FAKE_LOG/n"
echo launch >> "$FAKE_LOG/launches"
case $n in
` + cases + `		*) exit 0 ;;
esac`
	return fakeRunner(t, body)
}

func TestRetryableExitRelaunches(t *testing.T) {
	logDir := t.TempDir()
	dir := loopRunner(t, logDir, []string{"2", "2"}, "")
	srv := apiServer(t)
	defer srv.Close()
	s := newSupervisor(t, dir, baseCfg(), srv, logDir)
	if code := s.Run(context.Background(), make(chan os.Signal)); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if n := strings.Count(readFile(t, filepath.Join(logDir, "launches")), "launch"); n != 3 {
		t.Errorf("launches = %d, want 3", n)
	}
}

func TestUpdateExitWaitsForMarkerThenRelaunches(t *testing.T) {
	logDir := t.TempDir()
	dir := loopRunner(t, logDir, []string{"3"}, `(sleep 0.2; touch update.finished) &`)
	srv := apiServer(t)
	defer srv.Close()
	s := newSupervisor(t, dir, baseCfg(), srv, logDir)
	start := time.Now()
	if code := s.Run(context.Background(), make(chan os.Signal)); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if n := strings.Count(readFile(t, filepath.Join(logDir, "launches")), "launch"); n != 2 {
		t.Errorf("launches = %d, want 2", n)
	}
	if time.Since(start) > 1900*time.Millisecond {
		t.Error("waited for the full timeout instead of seeing update.finished")
	}
	if _, err := os.Stat(filepath.Join(dir, "update.finished")); err == nil {
		t.Error("update.finished marker was not consumed")
	}
}

func TestFatalAndSessionConflictAreNotRetried(t *testing.T) {
	for _, code := range []string{"1", "5"} {
		logDir := t.TempDir()
		dir := loopRunner(t, logDir, []string{code}, "")
		srv := apiServer(t)
		s := newSupervisor(t, dir, baseCfg(), srv, logDir)
		got := s.Run(context.Background(), make(chan os.Signal))
		srv.Close()
		if want := int(code[0] - '0'); got != want {
			t.Errorf("exit %s: got %d", code, got)
		}
		if n := strings.Count(readFile(t, filepath.Join(logDir, "launches")), "launch"); n != 1 {
			t.Errorf("exit %s: launches = %d, want 1", code, n)
		}
	}
}

func TestCustomCommandRunsOnceAfterRegistration(t *testing.T) {
	logDir := t.TempDir()
	dir := fakeRunner(t, "exit 0")
	srv := apiServer(t)
	defer srv.Close()
	cfg := baseCfg()
	cfg.Command = []string{"/bin/sh", "-c", "echo custom > \"$FAKE_LOG/custom\"; exit 9"}
	s := newSupervisor(t, dir, cfg, srv, logDir)
	if code := s.Run(context.Background(), make(chan os.Signal)); code != 9 {
		t.Fatalf("exit code = %d, want 9", code)
	}
	if strings.TrimSpace(readFile(t, filepath.Join(logDir, "custom"))) != "custom" {
		t.Error("custom command did not run")
	}
}
