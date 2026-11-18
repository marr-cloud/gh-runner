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

> **The Docker socket is root on the host.** Anything that can run a job can take over the machine. Use it only for repositories you trust, on dedicated hosts. For untrusted code, use rootless Docker or a Docker-in-Docker sidecar per runner instead.

Docker Desktop (macOS/Windows) exposes the socket with a different group; check `stat` inside the VM or run the runner as `user: root`.
