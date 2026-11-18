# Migrating from myoung34/github-runner

Most variables carry over. These differences matter:

| Upstream | Here |
|---|---|
| `RUNNER_NAME=foo` used as the exact name | `RUNNER_NAME` is a **prefix**; the name is `foo-<container id>`. `RUNNER_NAME_SUFFIX=none` restores exact names (but then you cannot scale) |
| `RANDOM_RUNNER_SUFFIX` | `RUNNER_NAME_SUFFIX=random\|hostname\|none`. `RANDOM_RUNNER_SUFFIX=true` is still read as `random` |
| `EPHEMERAL=false` turned it **on** (any non-empty value did) | Real booleans: `false` is false |
| `LABELS` defaulted to `default` | No default; the runner's own defaults apply |
| `RUN_AS_ROOT=true` by default | Runs as uid 1001. Use `user: root` in Compose if you must |
| `START_DOCKER_SERVICE` | Removed: there is no dockerd in the image. Mount the host socket ([Docker jobs](Docker-jobs)) or run a DinD sidecar |
| `UNSET_CONFIG_VARS` | Removed: credentials are always scrubbed |
| `CONFIGURED_ACTIONS_RUNNER_FILES_DIR` (runner reuse) | Removed. Use `EPHEMERAL=true` |
| `DEBUG_ONLY`, `DEBUG_OUTPUT` | `GHR_DRY_RUN=true` |
| `APP_LOGIN` | Not needed; the installation is found from the repository or organisation |
| The wrapper script and `runner_id.txt` volume for `--scale` | Not needed |
| `apt`, `sudo`, many preinstalled tools | Not in the image; see [Building and extending](Building-and-extending) |

Also changed: deregistration uses GitHub's `remove-token` endpoint, and GitHub App installations are looked up directly instead of paging through `/app/installations`.
