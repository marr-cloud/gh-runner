# Docker jobs

For workflows that run `docker build`, container actions or service containers, use the `-docker` image with the host's Docker socket:

```bash
export DOCKER_GID=$(stat -c %g /var/run/docker.sock)
sudo install -d -o 1001 -g 1001 /tmp/runners
docker compose -f compose.yaml -f compose.docker.yaml up -d --scale runner=4
```

The override does four things:

1. Uses the `-docker` image, which adds the static `docker` CLI.
2. Adds the socket's group to the container (`group_add`). The runner stays a non-root user.
3. Mounts the socket.
4. Mounts `/tmp/runners` at the **same path** inside and outside the container and points `RUNNER_WORKDIR_ROOT` at it. Docker resolves bind-mount paths on the host, so a job that does `-v $GITHUB_WORKSPACE:/work` only works if the path means the same thing on both sides. Each replica uses `/tmp/runners/<runner name>`.

The directory must exist and belong to uid 1001 before `up` (the `install -d` line above). The override mounts it with `create_host_path: false`, so if it is missing Compose stops with `bind source path does not exist` instead of Docker silently creating it as root.

> **The Docker socket is root on the host.** Anything that can run a job can take over the machine. Use it only for repositories you trust, on dedicated hosts. For untrusted code, use rootless Docker or a Docker-in-Docker sidecar per runner instead.

Docker Desktop (macOS/Windows) exposes the socket as `root:root` with mode `660` inside its VM, so use `DOCKER_GID=0`. Check with `docker run --rm -v /var/run/docker.sock:/s busybox stat -c %g /s`, and create the work directory inside the VM the same way: `docker run --rm -v /tmp:/h busybox install -d -o 1001 -g 1001 /h/runners`.
