# Architecture

## What `ghr` does

1. Reads its configuration from the environment and validates it. Fails fast with exit code 2 on bad input.
2. Removes stale registration files left in the container's writable layer by a previous run.
3. Gets a registration token (PAT, or GitHub App JWT then installation token).
4. Runs `bin/Runner.Listener configure --unattended --replace ...`.
5. Runs `bin/Runner.Listener run`, forwarding `SIGTERM`, `SIGINT`, `SIGQUIT` and `SIGHUP` to it.
6. On exit, gets a remove token and runs `bin/Runner.Listener remove`, unless the runner already removed itself (ephemeral runners do).

The container's exit code is the runner's.

## Why not `config.sh` and `run.sh`?

They are bash wrappers that:

- run `ldd` and `ldconfig -NXv | grep libicu` before configuring, which cannot work in an image with no `ldd`, no `ldconfig` and no `ld.so.cache`;
- implement a restart loop in `run-helper.sh`;
- do not forward signals unless `RUNNER_MANUALLY_TRAP_SIG` is set.

`ghr` does the same work in Go, in a few dozen lines, so the image needs none of that machinery.

## Exit codes of the listener

`ghr` handles them like the runner's own `run-helper.sh`:

| Code | Meaning | `ghr` |
|---|---|---|
| 0 | clean stop | exit 0 |
| 1 | fatal error | exit 1 |
| 2 | retryable error | wait 5 s, relaunch |
| 3, 4 | the runner is updating itself | wait up to 30 s for `update.finished`, relaunch |
| 5 | session conflict (same name registered twice) | exit 5 |

A signal received while the listener runs is forwarded and the listener is *not* relaunched.

## Self-update

GitHub requires runners to stay reasonably current, and by default the runner downloads and applies updates itself. In an ephemeral container that is wasted work; the image is rebuilt weekly instead, so `DISABLE_AUTO_UPDATE=true` is a good choice if you keep images fresh. If you leave auto-update on, `ghr` waits for the update to finish and relaunches the runner, as `run.sh` would.

## Image layout

```
/usr/local/bin/ghr          the entrypoint
/actions-runner             the runner (owned by uid 1001)
/_work                      default work root
/opt/hostedtoolcache        tool cache for setup-* actions
/usr/bin/{bash,git,curl,..} chiselled Ubuntu packages (see slices/)
```

The runner tarball is 683 MB extracted; the image keeps about 300 MB of it (about 200 MB without Node 20) by dropping the musl/Alpine Node builds, npm and corepack, Node headers, and the shell wrappers.
