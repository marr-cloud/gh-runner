# Troubleshooting

**`invalid configuration` at start.** `ghr` validates before doing anything; the message names the variable. Try `--dry-run`.

**`HTTP 401/403/404` getting the registration token.** The credential lacks the permission for that scope, or the org/repo name is wrong. See [Authentication](Authentication). A 404 on a repo usually means the token cannot see it.

**Runners show up Offline after `docker compose down`.** The container was killed before it could deregister. Keep `stop_grace_period: 2m`. Stale entries can be deleted in GitHub; `--replace` handles re-registration under the same name.

**Runners named `GitHub Actions 123...-<id>`.** You ran `docker compose` from inside a GitHub Actions job (a deploy workflow, for example). The runner running that job exports `RUNNER_NAME`, and Compose reads the shell before `.env`. Set `RUNNER_NAME` explicitly in that step's `env:`.

**Two runners with the same name / `Session conflict`.** Do not set `RUNNER_NAME_SUFFIX=none` with more than one replica.

**`docker: permission denied` in a job.** `DOCKER_GID` does not match the socket's group. Check `stat -c %g /var/run/docker.sock`.

**`create workdir ... permission denied`, container restarting.** The work directory's parent is not writable by uid 1001, usually a bind mount Docker created as root. The log's `hint` gives the exact `install -d` command to run on the host.

**`bind source path does not exist: /tmp/runners`.** Create it first; see [Docker jobs](Docker-jobs).

**Container actions fail to mount the workspace.** The work directory must exist at the same path on the host and in the container; see [Docker jobs](Docker-jobs).

**A job needs a tool that is not installed.** Add its slice or use a different base; see [Building and extending](Building-and-extending).

**Zombie processes accumulate.** Run with `init: true` (set in `compose.yaml`) or `docker run --init`.

## Known limitations

- On `noble` no Kerberos libraries are included, so Kerberos/NTLM-authenticated proxies are not supported (`resolute` ships them with curl).
- The runner's self-update path (exit codes 3 and 4) is handled like `run-helper.sh` does, waiting up to 30 seconds for the update script. An update lives only in the container's writable layer and is lost on the next container, so prefer `DISABLE_AUTO_UPDATE=true` with weekly image refreshes.
- `git` has no hook templates (`warning: templates not found` on `git init`); harmless.
