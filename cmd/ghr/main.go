// Command ghr is the entrypoint of the gh-runner images. It registers a
// GitHub Actions runner, runs it, and deregisters it on exit.
//
// Usage: ghr [--dry-run] [--version] [-- COMMAND [ARGS...]]
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() { os.Exit(realMain(os.Args[1:])) }

func realMain(args []string) int {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 {
				return slog.Attr{} // the container runtime timestamps lines
			}
			return a
		},
	}))

	dryRun := os.Getenv("GHR_DRY_RUN") == "true"
parse:
	for len(args) > 0 {
		switch a := args[0]; {
		case a == "--version" || a == "-version" || a == "version":
			fmt.Println("ghr", version)
			return 0
		case a == "--help" || a == "-h" || a == "help":
			fmt.Fprintln(os.Stderr, "usage: ghr [--dry-run] [--version] [-- COMMAND [ARGS...]]\nsee https://github.com/marr-cloud/gh-runner for configuration")
			return 0
		case a == "--dry-run":
			dryRun = true
			args = args[1:]
		case a == "--":
			args = args[1:]
			break parse
		case strings.HasPrefix(a, "-"):
			log.Error("unknown flag", "flag", a, "hint", "use -- to run a command that starts with a dash")
			return 2
		default:
			break parse // the rest is a command to run instead of the runner
		}
	}

	host, _ := os.Hostname()
	cfg, err := LoadConfig(Env{Get: os.Getenv, ReadFile: os.ReadFile, Hostname: host}, args)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		return 2
	}
	if dryRun {
		fmt.Println(cfg.describe())
		return 0
	}
	if os.Getpid() == 1 {
		log.Debug("running as PID 1; use `init: true` / `docker run --init` to reap orphaned job processes")
	}

	sup := &Supervisor{
		cfg:     cfg,
		api:     NewClient(cfg),
		log:     log,
		environ: childEnv(os.Environ(), os.Getuid()),

		retryDelay: 5 * time.Second,
		updateWait: 30 * time.Second,
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)
	return sup.Run(context.Background(), sigs)
}
