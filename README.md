# gh-runner

Small, auditable GitHub Actions self-hosted runner images on **chiselled Ubuntu 24.04 (`noble`) and 26.04 (`resolute`)**, with an entrypoint written as one dependency-free Go binary and native `docker compose up --scale` support.

```bash
cp .env.example .env          # ORG_NAME (or REPO_URL) and ACCESS_TOKEN
docker compose up -d --scale runner=3
```

- **No shell scripts.** `ghr` (`cmd/ghr`, about 900 lines, standard library only) registers the runner, forwards signals, restarts it like the runner's own wrapper would, and deregisters on exit.
- **Small.** `FROM scratch` plus a [chisel](https://github.com/canonical/chisel)-cut rootfs and a pruned runner (the upstream tarball ships 683 MB; the image keeps about 300 MB, or about 200 MB without Node 20).
- **Scales natively.** Runner names come from the container hostname, so replicas never collide and need no volume or wrapper.
- **Safer defaults.** Non-root, credentials scrubbed from job environments, checksum-verified runner, SBOM and provenance on release builds.

| Image | |
|---|---|
| `meitrix8208/gh-runner:noble` · `:resolute` | Runner + bash, coreutils, git, curl, jq, tar |
| `meitrix8208/gh-runner:noble-docker` · `:resolute-docker` | Same + docker CLI (host socket) |

Documentation lives in [`docs/`](docs/Home.md) and is published to the wiki: [configuration](docs/Configuration.md), [authentication](docs/Authentication.md), [scaling](docs/Scaling.md), [Docker jobs](docs/Docker-jobs.md), [security](docs/Security.md), [architecture](docs/Architecture.md), [migrating from myoung34](docs/Migrating-from-myoung34.md).

This project started as a wrapper around [myoung34/docker-github-actions-runner](https://github.com/myoung34/docker-github-actions-runner) and no longer uses any of its code or images.

## Development

```bash
go vet ./... && go test -race ./...
```
