---
status: accepted
amended-by: 0004 (type, options, secrets and public moved from the mount to an [[account]] block), 0006 (type back on the mount)
---
# Backend-neutral config: pass options through to rclone, validate against rclone's metadata

lagra mounts many kinds of remote storage but must not accumulate per-protocol code. Each mount has a `type` (an rclone backend name), a `path` in rclone notation, and two opaque key/value blocks: `options` (backend) and `vfs` (VFS layer). lagra passes both through 1:1 and validates them against the option metadata rclone itself exposes (required, sensitive, type, allowed values) instead of maintaining its own field lists. Sensitive options (rclone's `Sensitive` flag, since e.g. no S3 credential is flagged `IsPassword`) must come from a secret source, one source per option.

## Considered options

- First-class fields per protocol (`bucket`, `endpoint`, `region`, ...): friendlier TOML, but every new backend needs code and the validator drifts from rclone. Rejected.
- One secret bundle (JSON) per mount: fewer keychain items, but a lagra-specific format to define and parse, and no per-key rotation. Rejected.

## Consequences

- One deliberate exception lives in the rclone adapter: a small table of *recommended* backend options that a Finder volume needs but rclone's defaults (tuned for sync jobs) do not set, currently `directory_markers = true` for S3. The user's own value always wins.
- A second exception, also in the adapter: the list of object stores (S3, Azure Blob, B2, Swift, ...) whose root lists buckets. rclone reports this only from a live backend, which validation must not create, so the adapter keeps the list; validation uses it to require a bucket in `path`.
- A third exception, also in the adapter: the backends lagra offers are the ones `internal/rclone/backends.go` imports, not rclone's `backend/all`. lagra opens a backend from option values and never runs rclone's interactive config, so backends that need a browser sign-in (OAuth) or a login step that stores tokens, and wrappers around another named remote, are left out until lagra has a sign-in flow. Adding a backend that logs in with plain option values means adding its import there, not writing code for it.
- A fourth exception, also in the adapter: FTP, SFTP and SMB join `host` with the port and dial it, so an address in its place (`smb://server/share`, `\\server\share`, `server:2222`) fails only when rclone connects, with "too many colons in address". Validation rejects it and says where each part goes; `Start` checks again, because a host kept as a secret never reaches validation. Other backends' `host` options are not names alone (NetStorage's carries a path) and are left alone.
- A fifth exception, also in the adapter: rclone's SMB backend retries a failed connection, a rejected login included, `low_level_retries` times, so a wrong password locks a directory account quickly. The adapter gives it one attempt and recognises go-smb2's login refusals, which the engine treats as a permanent failure. It also opens SMB paths as folders (with a trailing slash), so that the backend does not connect in `NewFs` and an unreachable server leaves an offline volume.
- rclone's `Sensitive` flag also covers identifiers (SFTP `host`, `user`). A mount may list such options under `public` to keep them in the file; options rclone marks as passwords never can be.
- `config.example.toml` documents options by pointing at rclone's backend docs rather than duplicating them.
