# gh-runner

Self-hosted **GitHub Actions runner** images on **chiselled Ubuntu**, built to be small, auditable and to scale with plain Docker Compose.

| | |
|---|---|
| Ubuntu releases | `noble` (24.04) and `resolute` (26.04) only |
| Entrypoint | `ghr`, one static Go binary, standard library only, no shell scripts |
| Rootfs | Cut with [chisel](https://github.com/canonical/chisel): no apt, no dpkg, no package manager |
| Scaling | `docker compose up --scale runner=N` works out of the box |
| Architectures | `linux/amd64`, `linux/arm64` |

## Quick start

```bash
git clone https://github.com/marr-cloud/gh-runner && cd gh-runner
cp .env.example .env        # fill in ORG_NAME (or REPO_URL) and ACCESS_TOKEN
docker compose up -d --scale runner=3
```

Three runners appear in your organisation, named `gh-runner-<container id>`. Scale up or down at any time with another `--scale`; stopping a container deregisters its runner.

Next: [Configuration](Configuration) · [Authentication](Authentication) · [Scaling](Scaling) · [Docker jobs](Docker-jobs) · [Security](Security)

## Images

| Tag | Contents |
|---|---|
| `noble`, `resolute` | Runner, bash, coreutils, git, curl, jq, tar, gzip, unzip, ssh client, CA certificates |
| `noble-docker`, `resolute-docker` | The above plus the static `docker` CLI, for jobs that use the host's Docker socket |
| `<tag>-<runner version>` | Pinned to a runner release, e.g. `noble-2.338.2` |
| `latest` | Same as `noble` |

Published to `docker.io/meitrix8208/gh-runner` and `ghcr.io/marr-cloud/gh-runner`. Rebuilt every Monday so Ubuntu security updates and new runner releases arrive without any action on your part.

## What is *not* in the image

There is no `apt`, `sudo`, compiler, Python or Node on `PATH` (Node for JavaScript actions is bundled inside the runner). That is the point of the image. If your workflows need more, see [Building and extending](Building-and-extending).
