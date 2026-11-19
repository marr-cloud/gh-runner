package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// secretEnv lists variables that must never reach the runner process, and
// therefore never reach workflow steps (which could print them).
var secretEnv = []string{
	"ACCESS_TOKEN", "RUNNER_TOKEN", "APP_ID", "APP_LOGIN", "APP_PRIVATE_KEY",
}

// staleFiles are runner registration artefacts left over in the container's
// writable layer when a container is restarted; they must not be reused.
var staleFiles = []string{
	".runner", ".runner_migrated", ".credentials",
	".credentials_migrated", ".credentials_rsaparams",
}

const (
	exitSignalled = 143 // 128 + SIGTERM
	listener      = "./bin/Runner.Listener"
)

// Supervisor registers the runner, runs it, and deregisters it on exit.
//
// It drives bin/Runner.Listener directly instead of the config.sh / run.sh
// wrappers shipped with the runner. Those wrappers need ldd, ldconfig and a
// full shell environment, and run.sh does not forward signals by default.
type Supervisor struct {
	cfg     *Config
	api     *Client
	log     *slog.Logger
	environ []string

	retryDelay time.Duration // wait before relaunching after exit code 2
	updateWait time.Duration // how long to wait for update.finished after exit 3/4
}

// Run executes the full lifecycle and returns the process exit code.
// Signals received on sigs are forwarded to the runner while it is running.
func (s *Supervisor) Run(ctx context.Context, sigs <-chan os.Signal) int {
	cfg := s.cfg
	if err := os.MkdirAll(cfg.Workdir, 0o755); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			// Typically a bind mount that Docker created as root.
			parent := filepath.Dir(cfg.Workdir)
			s.log.Error("create workdir", "dir", cfg.Workdir, "err", err,
				"hint", fmt.Sprintf("%s must be writable by uid %d; on the host run: sudo install -d -o %d -g %d %s",
					parent, os.Getuid(), os.Getuid(), os.Getgid(), parent))
			return 1
		}
		s.log.Error("create workdir", "dir", cfg.Workdir, "err", err)
		return 1
	}
	for _, f := range staleFiles {
		_ = os.Remove(filepath.Join(cfg.RunnerDir, f))
	}

	token := cfg.RunnerToken
	if token == "" {
		var err error
		if token, err = s.api.RegistrationToken(ctx); err != nil {
			s.log.Error("get registration token", "err", err)
			return 1
		}
	}

	s.log.Info("registering runner", "name", cfg.Name, "url", cfg.ServerURL, "workdir", cfg.Workdir)
	code, interrupted := s.configure(ctx, sigs, token)
	switch {
	case interrupted:
		// A signal arrived during registration: undo it and stop.
		if !cfg.KeepRegistered {
			s.deregister()
		}
		return exitSignalled
	case code != 0:
		s.log.Error("runner configuration failed", "exit", code)
		return code
	}

	code = s.runRunner(sigs)

	if !cfg.KeepRegistered {
		s.deregister()
	}
	return code
}

func (s *Supervisor) configure(ctx context.Context, sigs <-chan os.Signal, token string) (code int, interrupted bool) {
	cfg := s.cfg
	args := []string{
		"configure", "--unattended", "--replace",
		"--url", cfg.ServerURL,
		"--token", token,
		"--name", cfg.Name,
		"--work", cfg.Workdir,
	}
	if cfg.Labels != "" {
		args = append(args, "--labels", cfg.Labels)
	}
	if cfg.Group != "" {
		args = append(args, "--runnergroup", cfg.Group)
	}
	if cfg.Ephemeral {
		args = append(args, "--ephemeral")
	}
	if cfg.DisableUpdate {
		args = append(args, "--disableupdate")
	}
	if cfg.NoDefaultLabels {
		args = append(args, "--no-default-labels")
	}

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := make(chan struct{})
	exited := make(chan struct{})
	gotSig := false
	go func() {
		defer close(exited)
		select {
		case <-sigs:
			gotSig = true
			cancel() // kills config.sh
		case <-stop:
		}
	}()

	cmd := s.command(cctx, listener, args...)
	err := cmd.Run()
	close(stop)
	<-exited // gotSig is safe to read after this
	if gotSig {
		return exitCode(err), true
	}
	return exitCode(err), false
}

// runRunner runs the listener until it stops for good. With a custom command
// (ghr -- CMD ...) it just runs that once.
func (s *Supervisor) runRunner(sigs <-chan os.Signal) int {
	if len(s.cfg.Command) > 0 {
		code, _ := s.runOnce(sigs, s.cfg.Command[0], s.cfg.Command[1:]...)
		return code
	}
	for {
		code, stopped := s.runOnce(sigs, listener, "run")
		if stopped {
			return code
		}
		// Exit codes are those handled by the runner's own run-helper.sh.
		switch code {
		case 2: // retryable error
			s.log.Info("runner exited with a retryable error; relaunching", "in", s.retryDelay)
			if !s.sleep(sigs, s.retryDelay) {
				return exitSignalled
			}
		case 3, 4: // the runner is updating itself
			s.log.Info("runner is updating; waiting for the update to finish")
			if !s.waitForUpdate(sigs) {
				return exitSignalled
			}
		default: // 0 clean stop, 1 fatal, 5 session conflict, anything else
			return code
		}
	}
}

// runOnce runs one process, forwarding signals to it. stopped reports that a
// signal was received, in which case the caller must not restart it.
func (s *Supervisor) runOnce(sigs <-chan os.Signal, name string, args ...string) (code int, stopped bool) {
	cmd := exec.Command(name, args...)
	s.attach(cmd)
	if err := cmd.Start(); err != nil {
		s.log.Error("start failed", "cmd", name, "err", err)
		return 127, false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case sig := <-sigs:
			stopped = true
			s.log.Info("forwarding signal to runner", "signal", sig.String())
			_ = cmd.Process.Signal(sig)
		case err := <-done:
			return exitCode(err), stopped
		}
	}
}

// sleep waits d, returning false if a signal arrived first.
func (s *Supervisor) sleep(sigs <-chan os.Signal, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-sigs:
		return false
	}
}

// waitForUpdate waits for the runner's update script to drop update.finished.
func (s *Supervisor) waitForUpdate(sigs <-chan os.Signal) bool {
	marker := filepath.Join(s.cfg.RunnerDir, "update.finished")
	deadline := time.Now().Add(s.updateWait)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			_ = os.Remove(marker)
			s.log.Info("update finished")
			return true
		}
		if !s.sleep(sigs, 500*time.Millisecond) {
			return false
		}
	}
	s.log.Warn("update did not finish in time; relaunching anyway", "waited", s.updateWait)
	return true
}

// deregister removes the runner from GitHub, unless the runner already did
// it itself (ephemeral runners remove their .runner file after one job).
func (s *Supervisor) deregister() {
	cfg := s.cfg
	if _, err := os.Stat(filepath.Join(cfg.RunnerDir, ".runner")); err != nil {
		return // nothing registered
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	token, err := s.api.RemoveToken(ctx)
	if err != nil {
		// With only a RUNNER_TOKEN we cannot mint a remove token.
		s.log.Warn("cannot deregister runner; remove it manually in GitHub", "name", cfg.Name, "err", err)
		return
	}
	s.log.Info("deregistering runner", "name", cfg.Name)
	cmd := s.command(ctx, listener, "remove", "--token", token)
	if err := cmd.Run(); err != nil {
		s.log.Warn("runner removal failed", "err", err)
	}
}

// command prepares a short-lived Runner.Listener invocation. On cancellation
// it terminates the whole process group so no helper process is orphaned.
func (s *Supervisor) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	s.attach(cmd)
	return cmd
}

func (s *Supervisor) attach(cmd *exec.Cmd) {
	cmd.Dir = s.cfg.RunnerDir
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = s.environ
}

// childEnv returns environ without secrets, plus what the runner needs.
func childEnv(environ []string, uid int) []string {
	drop := make(map[string]bool)
	for _, k := range secretEnv {
		drop[k] = true
		drop[k+"_FILE"] = true
	}
	out := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	if uid == 0 {
		// The runner refuses to run as root otherwise.
		out = append(out, "RUNNER_ALLOW_RUNASROOT=1")
	}
	return out
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 1
}

// describe returns a human summary of the resolved config with no secrets.
func (c *Config) describe() string {
	cred := "runner-token"
	switch {
	case c.AppID != "":
		cred = "github-app"
	case c.AccessToken != "":
		cred = "access-token"
	}
	return fmt.Sprintf(
		"scope=%s url=%s api=%s name=%s workdir=%s labels=%q group=%q ephemeral=%t auth=%s command=%q",
		c.Scope, c.ServerURL, c.APIBase, c.Name, c.Workdir, c.Labels, c.Group, c.Ephemeral, cred, c.Command,
	)
}
