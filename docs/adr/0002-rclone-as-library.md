---
status: accepted
---
# Embed rclone as a Go library instead of driving an rclone process

lagra needs rclone's backends, VFS and NFS server. Two viable shapes: import the packages into one binary, or run one `rclone rcd` over a Unix socket and drive it through the rc API (grrclone's approach). The spike in `hack/spike` (since removed) showed the import works with pure Go (`CGO_ENABLED=0`), builds in seconds, and gives direct access to `vfs.Stats()`, server lifecycle and option metadata without JSON plumbing. We import.

## Considered options

- **rclone rcd + rc API**: decouples from rclone's unstable Go API and lets the user upgrade rclone independently, but adds a child process to supervise, socket auth, and a second failure domain. Kept as documented fallback; the `Starter`/`Instance` interfaces in `internal/rclone` are shaped so an rc-backed implementation could replace the in-process one.
- **One rclone process per mount**: simplest to reason about but N processes, N caches, and no shared statistics. Rejected.

## Consequences

- rclone's Go API has no stability promise: the module version is pinned and upgraded deliberately, with `go test ./internal/rclone/` as the smoke test: it starts a real server on a local directory and counts pending uploads through rclone's VFS.
- Binary is large (~100 MB with all backends, ~80 MB with only the ones lagra offers, see ADR 0001). Acceptable for a menu-bar app.
- The VFS cache root is process-global, so `cache_dir` lives in `[general]`, not per mount.
- Backend creation can block on network retries, so it always runs under a deadline in the Connecting state.
