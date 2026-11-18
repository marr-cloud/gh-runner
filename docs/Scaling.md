# Scaling

```bash
docker compose up -d --scale runner=10     # now 10 runners
docker compose up -d --scale runner=2      # down to 2; the others deregister cleanly
```

or set `RUNNERS=10` in `.env` (the `scale:` key in `compose.yaml`).

## Why it just works

The old approach needed a wrapper script and an anonymous volume holding a random id so replicas would not collide. Here the runner name is `<RUNNER_NAME>-<hostname>`, and a container's hostname is its container id, unique by construction. There is no per-replica state, no `container_name`, and no published ports, which is exactly what Compose needs to replicate a service.

Each replica also gets its own work directory (`<RUNNER_WORKDIR_ROOT>/<runner name>`), so replicas sharing a bind mount do not overwrite each other.

## Recommended settings

- `EPHEMERAL=true` (the default in `compose.yaml`): each container takes exactly one job and exits, and `restart: unless-stopped` brings up a clean replacement. Jobs never see state left by a previous job.
- `stop_grace_period: 2m`, already set: on `docker compose stop`, the runner gets time to cancel its job and deregister. Without a grace period Docker kills it after 10 seconds and the runner stays listed as offline in GitHub.
- `init: true`, already set: reaps processes orphaned by workflow steps.

## Beyond one host

Compose scales on one machine. For more, run the same image under Kubernetes (a Deployment with `EPHEMERAL=true` is enough) or Nomad; nothing in `ghr` depends on Compose.

## Checking it

```bash
GHR_DRY_RUN=true RESTART_POLICY=no docker compose up --scale runner=3
docker compose logs --no-log-prefix | grep -o ' name=[^ ]*'
```

prints three distinct runner names without contacting GitHub. CI runs exactly this on every build.
