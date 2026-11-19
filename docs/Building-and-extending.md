# Building and extending

## Build locally

```bash
# find the runner version and its checksums
v=2.338.2
sha_amd64=$(curl -fsSL https://github.com/actions/runner/releases/download/v$v/actions-runner-linux-x64-$v.tar.gz | sha256sum | cut -d' ' -f1)
sha_arm64=$(curl -fsSL https://github.com/actions/runner/releases/download/v$v/actions-runner-linux-arm64-$v.tar.gz | sha256sum | cut -d' ' -f1)

docker buildx build \
  --build-arg UBUNTU_RELEASE=noble \
  --build-arg RUNNER_VERSION=$v \
  --build-arg RUNNER_SHA256_AMD64=$sha_amd64 \
  --build-arg RUNNER_SHA256_ARM64=$sha_arm64 \
  --target slim -t gh-runner:noble .
```

`--target docker` adds the Docker CLI. Other build arguments: `NODE_VERSIONS` (`"20 24"`; drop `20` to save about 100 MB if none of your actions still use it), `CHISEL_VERSION`, `GO_IMAGE`.

## Add tools

Tools come from `slices/<release>.txt`, one chisel slice per line. Find slice names in [chisel-releases](https://github.com/canonical/chisel-releases) (`slices/<package>.yaml`) and add them, for example `gawk_bins` or `zip_bins`, then rebuild. One slice per line, comments only on their own lines. If a package has no slice you need, you can contribute one upstream, or copy the files from the release's package in the `rootfs` stage, as the Dockerfile does for `git-submodule`. Packages added as slices show up in `/var/lib/dpkg/status.d/` automatically.

## End-to-end check

The CI smoke tests never talk to GitHub. To see a real job run on a local build, register an ephemeral runner with a unique label against a repository you own and push a workflow with `runs-on: [self-hosted, <label>]`:

```bash
ACCESS_TOKEN=$(gh auth token) docker run --rm --init -e ACCESS_TOKEN \
  -e REPO_URL=https://github.com/OWNER/REPO -e LABELS=my-e2e -e EPHEMERAL=true \
  gh-runner:noble
```

It takes one job, then removes itself from the repository.

## When chisel is not enough

If your jobs need `apt`, `sudo`, compilers or Python, a chiselled image is the wrong base. Reuse just the runner and `ghr` on a normal Ubuntu image instead:

```dockerfile
FROM ubuntu:noble
RUN apt-get update && apt-get install -y --no-install-recommends \
      libicu74 libssl3t64 zlib1g ca-certificates git curl jq build-essential sudo \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -u 1001 -m runner
COPY --from=ghcr.io/marr-cloud/gh-runner:noble --chown=1001:1001 /actions-runner /actions-runner
COPY --from=ghcr.io/marr-cloud/gh-runner:noble /usr/local/bin/ghr /usr/local/bin/ghr
USER runner
WORKDIR /actions-runner
ENTRYPOINT ["/usr/local/bin/ghr"]
```

Use `libicu78` on `resolute`.

## Tests

```bash
go vet ./... && go test -race ./...
```

The tests cover configuration, the GitHub API client (including a real RS256 verification of the App JWT), and the whole lifecycle against a fake `Runner.Listener`: registration, signal forwarding, deregistration, restart codes, secret scrubbing.
