# syntax=docker/dockerfile:1.7
#
# GitHub Actions runner on chiselled Ubuntu.
#
#   docker build --build-arg UBUNTU_RELEASE=noble    --target slim   .
#   docker build --build-arg UBUNTU_RELEASE=resolute --target docker .
#
# Targets:  slim    runner + shell + git/curl/jq/tar.   (default)
#           docker  slim + the static docker CLI, for jobs that use the host's socket.
#
# The final image is built FROM scratch from a rootfs cut with chisel
# (https://github.com/canonical/chisel): no apt, no dpkg, no package manager.

ARG UBUNTU_RELEASE=noble
ARG GO_IMAGE=golang:1.25-bookworm
ARG DOCKER_CLI_IMAGE=docker:29-cli

# ---------------------------------------------------------------- ghr ------
# The entrypoint. Pure standard library, static, reproducible (-trimpath).
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS ghr
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/ghr ./cmd/ghr

# ------------------------------------------------------------- chisel ------
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS chisel
ARG CHISEL_VERSION=v1.5.1
RUN CGO_ENABLED=0 GOBIN=/out go install -trimpath github.com/canonical/chisel/cmd/chisel@${CHISEL_VERSION}

# ------------------------------------------------------------- runner ------
# Download the official runner, verify it, and strip what a Linux glibc
# container never uses (musl/alpine Node builds, npm, node headers...).
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS runner
ARG TARGETARCH
ARG RUNNER_VERSION
ARG RUNNER_SHA256_AMD64
ARG RUNNER_SHA256_ARM64
# Node versions the runner bundles for JavaScript actions. Dropping "20"
# saves ~100 MB, but actions that still declare `using: node20` will fail.
ARG NODE_VERSIONS="20 24"
RUN <<'EOF'
set -eu
test -n "${RUNNER_VERSION}" || { echo "RUNNER_VERSION is required" >&2; exit 1; }
case "${TARGETARCH}" in
  amd64) arch=x64;   sha="${RUNNER_SHA256_AMD64}" ;;
  arm64) arch=arm64; sha="${RUNNER_SHA256_ARM64}" ;;
  *) echo "unsupported arch ${TARGETARCH}" >&2; exit 1 ;;
esac
test -n "${sha}" || { echo "RUNNER_SHA256_$(echo "${TARGETARCH}" | tr a-z A-Z) is required" >&2; exit 1; }
curl -fsSL -o /tmp/runner.tgz \
  "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-${arch}-${RUNNER_VERSION}.tar.gz"
echo "${sha}  /tmp/runner.tgz" | sha256sum -c -
mkdir /actions-runner
tar -xzf /tmp/runner.tgz -C /actions-runner
cd /actions-runner
# ghr drives bin/Runner.Listener directly, so the bash wrappers go away.
rm -f config.sh run.sh env.sh run-helper.sh.template run-helper.cmd.template safe_sleep.sh \
      bin/installdependencies.sh bin/RunnerService.js \
      bin/libcoreclrtraceptprovider.so
rm -rf externals/*_alpine
for d in externals/node*; do
  v="${d#externals/node}"; keep=""
  for want in ${NODE_VERSIONS}; do [ "$v" = "$want" ] && keep=1; done
  [ -n "$keep" ] || rm -rf "$d"
done
rm -rf externals/node*/lib/node_modules externals/node*/include externals/node*/share \
       externals/node*/bin/npm externals/node*/bin/npx externals/node*/bin/corepack
chown -R 1001:1001 /actions-runner
EOF

# ------------------------------------------------------------- rootfs ------
# Cut the Ubuntu packages we need into /rootfs, add the runner user, and
# record what was installed so image scanners can see it. Runs on the build
# platform: chisel fetches packages for TARGETARCH without emulation.
FROM --platform=$BUILDPLATFORM ubuntu:${UBUNTU_RELEASE} AS rootfs
ARG UBUNTU_RELEASE
ARG TARGETARCH
RUN apt-get update -qq \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends ca-certificates jq zstd >/dev/null
COPY --from=chisel /out/chisel /usr/local/bin/chisel
COPY slices/${UBUNTU_RELEASE}.txt /slices.txt
RUN <<'EOF'
set -eu
case "${UBUNTU_RELEASE}" in
  noble)    release=ubuntu-24.04 ;;
  resolute) release=ubuntu-26.04 ;;
  *) echo "UBUNTU_RELEASE must be noble or resolute" >&2; exit 1 ;;
esac
mkdir /rootfs
chisel cut --release "${release}" --arch "${TARGETARCH}" --root /rootfs \
  $(grep -v '^[[:space:]]*#' /slices.txt | grep -v '^[[:space:]]*$')

# `git submodule` is a shell script that no chisel slice ships, and
# actions/checkout runs it on every checkout. Take it and the two shell
# libraries it sources from the release's signed git package (they are
# architecture-independent).
cd /tmp && apt-get download -qq git && dpkg-deb -x git_*.deb /tmp/git
for f in git-submodule git-sh-setup git-sh-i18n; do
  cp -a "/tmp/git/usr/lib/git-core/$f" /rootfs/usr/lib/git-core/
done

# Scanners (trivy, grype, syft) do not read chisel's manifest. Describe each
# package in /var/lib/dpkg/status.d/, the format distroless images use. The
# Source field maps binary packages to Ubuntu security notices.
mkdir -p /rootfs/var/lib/dpkg/status.d
zstd -dc /rootfs/var/lib/chisel/manifest.wall \
  | jq -r 'select(.kind == "package") | "\(.name) \(.version) \(.arch)"' \
  | while read -r name version arch; do
      src=$(apt-cache show "${name}=${version}" 2>/dev/null | sed -n 's/^Source: //p' | head -1)
      [ -n "${src}" ] || src=$(apt-cache show "${name}" 2>/dev/null | sed -n 's/^Source: //p' | head -1)
      {
        echo "Package: ${name}"
        echo "Status: install ok installed"
        [ -z "${src}" ] || echo "Source: ${src}"
        echo "Version: ${version}"
        echo "Architecture: ${arch}"
      } > "/rootfs/var/lib/dpkg/status.d/${name}"
    done
test -n "$(ls /rootfs/var/lib/dpkg/status.d)"

# /bin/sh -> dash comes from dash_bins. Add the unprivileged runner user.
echo 'runner:x:1001:1001:runner:/home/runner:/bin/bash' >> /rootfs/etc/passwd
echo 'runner:x:1001:'                                   >> /rootfs/etc/group
# /actions-runner must exist here: COPY --from creates a missing destination
# directory as root, and the runner writes _diag/, .runner, ... into it.
mkdir -p /rootfs/home/runner /rootfs/_work /rootfs/opt/hostedtoolcache /rootfs/actions-runner
chown 1001:1001 /rootfs/home/runner /rootfs/_work /rootfs/opt/hostedtoolcache /rootfs/actions-runner
chmod 1777 /rootfs/tmp
EOF

# --------------------------------------------------------------- slim ------
FROM scratch AS slim
ARG VERSION=dev
ARG RUNNER_VERSION
LABEL org.opencontainers.image.title="gh-runner" \
      org.opencontainers.image.description="GitHub Actions self-hosted runner on chiselled Ubuntu" \
      org.opencontainers.image.source="https://github.com/marr-cloud/gh-runner" \
      org.opencontainers.image.version="${VERSION}" \
      io.github.actions-runner.version="${RUNNER_VERSION}"
COPY --from=rootfs /rootfs/ /
COPY --from=ghr    /out/ghr /usr/local/bin/ghr
COPY --from=runner /actions-runner /actions-runner

ENV PATH=/usr/local/bin:/usr/bin:/bin \
    HOME=/home/runner \
    LANG=C.UTF-8 \
    AGENT_TOOLSDIRECTORY=/opt/hostedtoolcache \
    RUNNER_TOOL_CACHE=/opt/hostedtoolcache

USER 1001:1001
WORKDIR /actions-runner
ENTRYPOINT ["/usr/local/bin/ghr"]

# ------------------------------------------------------------- docker ------
FROM ${DOCKER_CLI_IMAGE} AS dockercli

FROM slim AS docker
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker

# The default target is the last stage, so keep `slim` last.
FROM slim
