# Security

## Runtime

- Runs as uid 1001 (`runner`), not root. No `sudo`, no setuid binaries, no package manager.
- The `ghr` binary strips credentials from the environment before starting the runner (see [Authentication](Authentication#keeping-secrets-away-from-workflows)).
- `EPHEMERAL=true` gives every job a fresh container.
- Self-hosted runners should not serve public repositories with untrusted pull requests. Anyone who can open a PR can run code on your runner.

## Supply chain

- The runner tarball is verified against a SHA-256 taken from the GitHub release, and the build fails if it does not match.
- Ubuntu packages come from the signed Ubuntu archive through chisel, and `chisel` itself is pinned (`CHISEL_VERSION`).
- Release builds publish an SBOM and SLSA provenance, and the GHCR image carries a GitHub build attestation:

```bash
gh attestation verify oci://ghcr.io/marr-cloud/gh-runner:noble --owner marr-cloud
```

- `ghr` has no third-party dependencies, so `go.mod` has no `require` lines. The whole program is about 900 lines in `cmd/ghr/`. Read it.

## Updates

Images are rebuilt weekly. A runner older than GitHub's support window is refused, so keep images fresh: `docker compose pull && docker compose up -d`, or let a tool such as Watchtower or Renovate do it.
