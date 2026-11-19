# Configuration

`ghr` is configured with environment variables. Run `docker run --rm -e ... IMAGE --dry-run` (or set `GHR_DRY_RUN=true`) to print the resolved configuration without contacting GitHub; secrets are never printed.

With Compose, every variable in `.env` is passed to the container, so anything on this page can go there.

Secrets (`ACCESS_TOKEN`, `RUNNER_TOKEN`, `APP_PRIVATE_KEY`) also accept a `_FILE` variant, e.g. `ACCESS_TOKEN_FILE=/run/secrets/token`, for Docker secrets.

## Where to register

| Variable | Default | Notes |
|---|---|---|
| `RUNNER_SCOPE` | `repo` (`org` in `compose.yaml`) | `repo`, `org` or `enterprise` |
| `REPO_URL` | | Required for `repo`, e.g. `https://github.com/owner/repo` |
| `ORG_NAME` | | Required for `org` |
| `ENTERPRISE_NAME` | | Required for `enterprise` |
| `GITHUB_HOST` | `github.com` | GitHub Enterprise Server host |
| `GITHUB_API_HOST`, `GITHUB_API_PATH` | derived | Only if your API is not at `api.github.com` or `HOST/api/v3` |

## Credentials (exactly one method)

| Variable | Notes |
|---|---|
| `ACCESS_TOKEN` | A personal access token. See [Authentication](Authentication) |
| `APP_ID` + `APP_PRIVATE_KEY` | A GitHub App. The key may be one line with literal `\n` |
| `RUNNER_TOKEN` | A one-hour registration token. Works, but the runner cannot be deregistered on exit |

## Identity

| Variable | Default | Notes |
|---|---|---|
| `RUNNER_NAME` | `gh-runner` | A *prefix*; the full name is `<RUNNER_NAME>-<suffix>` |
| `RUNNER_NAME_SUFFIX` | `hostname` | `hostname` (the container id, unique per replica), `random`, or `none` |
| `LABELS` / `RUNNER_LABELS` | runner defaults | Comma separated, added to `self-hosted,linux,x64` |
| `RUNNER_GROUP` | `Default` | Org and enterprise scope |
| `RUNNER_WORKDIR` | | Use exactly this directory |
| `RUNNER_WORKDIR_ROOT` | `/_work` | The work directory is `<root>/<runner name>` |

## Behaviour (booleans accept `true/false/1/0/yes/no/on/off`)

| Variable | Default | Notes |
|---|---|---|
| `EPHEMERAL` | `false` (`true` in `compose.yaml`) | Take one job, then exit. Compose restarts the container with a fresh runner. Recommended |
| `DISABLE_AUTO_UPDATE` | `false` | Pass `--disableupdate`. See [Architecture](Architecture#self-update) |
| `NO_DEFAULT_LABELS` | `false` | Do not add `self-hosted`, OS and architecture labels |
| `DISABLE_AUTOMATIC_DEREGISTRATION` | `false` | Leave the runner registered when the container stops |
| `GHR_DRY_RUN` | `false` | Print the resolved config and exit |
| `GHR_RUNNER_DIR` | `/actions-runner` | Where the runner lives |

## Command line

```
ghr [--dry-run] [--version] [-- COMMAND [ARGS...]]
```

With a command, `ghr` registers the runner, runs the command once instead of the runner, then deregisters. This is mostly useful for debugging.
