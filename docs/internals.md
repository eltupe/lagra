# Internals

How Lagra uses rclone, go-nfs and macOS, and why the code looks the way it does. Facts here were checked against the sources (rclone v1.75.1, the pinned go-nfs commit, Apple's man pages) or observed with real buckets. Decisions and their alternatives are in [`adr/`](adr/).

## Architecture

Each mount gets its own NFS server on `127.0.0.1`, built from rclone packages inside the Lagra process, and macOS mounts it with its own `mount_nfs`:

```
Finder ⇄ mount_nfs (NFSv3, soft) ⇄ go-nfs server ⇄ rclone VFS + write cache ⇄ rclone backend ⇄ storage
```

One coordinator goroutine per mount owns its state machine (disconnected, connecting, connected, offline, broken, failed; see `internal/engine`). It probes the volume locally and the backend remotely, repairs broken volumes with backoff, and applies config reloads as a per-mount diff.

## rclone as a library

- `cmd/serve/nfs`, `vfs`, `fs` and `backend/*` import cleanly. The NFS server is pure Go (`willscott/go-nfs`).
- A backend is built without `rclone.conf`: `fs.Find(type)` → `fs.ConfigMap(ri.Prefix, ri.Options, "", configmap.Simple(values))` → `ri.NewFs(ctx, name, path, cm)`. The `ConfigMap` step is required; passing the raw map skips rclone's defaults (`s3: chunk size: 0 is less than 5Mi`). Backend environment variables (`RCLONE_S3_*`) are still consulted by that map.
- `NewFs` for S3 performs a request and blocks through the AWS SDK's retries when the endpoint is unreachable, so backend creation runs under a deadline in the Connecting state, never on the coordinator goroutine.
- VFS options: copy `vfscommon.Opt`, apply values with `configstruct.Set(configmap.Simple(m), &opt)` using rclone's snake-case keys (`vfs_cache_mode`, `dir_cache_time`, ...), then `opt.Init(ctx)`.
- NFS server: `nfs.NewServer(ctx, vfs, &opt)` with `ListenAddr = "localhost:0"` picks a free port. `nfs_cache_type = disk` plus `nfs_cache_dir` keep file handles stable across restarts.
- Pending uploads come from the write-back queue (rc `vfs/queue`), not from `vfs.Stats()`: Stats walks every directory under that directory's lock, and rclone holds the lock while it lists the directory from the storage. With the storage frozen, one Stats call waited for minutes and took the mount's coordinator with it, so the mount never went offline. `vfs.WaitForWriters` walks the same way, so Lagra bounds it with a timeout before `Shutdown()`.
- The VFS cache root is global to the process (`config.GetCacheDir()`), not per VFS; rclone separates remotes underneath it. That is why `cache_dir` lives in `[general]`. Per-mount limits still work because `vfs_cache_max_size` is per VFS.
- The VFS cache opens its directory through the `local` backend, and rclone's `fs/operations` imports `backend/crypt`. Both are registered even though Lagra imports only the backends it offers (`internal/rclone/backends.go`), so Lagra hides them.
- rclone's `cmd` package, imported through `cmd/serve/nfs`, registers global flags on `pflag.CommandLine` at init. Lagra replaces `pflag.CommandLine` before building its own command tree so they do not leak into `lagra --help`.
- Binary size: about 39 MB with `backend/s3` alone, 102 MB with `backend/all`, 80 MB with the backends Lagra offers.

## Backend logins

- rclone keeps options marked `IsPassword` obscured in `rclone.conf`, and backends reveal them with `obscure.Reveal` when they open. A password passed in the clear fails ("input too short when revealing password - is it obscured?"), so Lagra obscures them before `NewFs`.
- rclone's SMB backend opens a connection whenever none is pooled and retries a failed one `low_level_retries` times (10), whatever the error, a rejected login included; it never pools a connection whose login failed. With a wrong password every Finder request cost ten failed logins, while Active Directory typically locks an account after five to ten. Its pacer is used only for opening connections, so Lagra gives the SMB backend one attempt (`connectRetries`, through the context `NewFs` gets) without touching file operations.
- go-smb2 reports a refused session as `*smb2.ResponseError` with the NTSTATUS code (`STATUS_LOGON_FAILURE`, `STATUS_ACCOUNT_LOCKED_OUT`, ...), and rclone wraps it with `%w`. Lagra turns those into `LoginRejectedError` in the start check and the backend probe, and the engine stops the mount as failed instead of keeping it offline. A share the server does not have (`STATUS_BAD_NETWORK_NAME`) is a missing path like a missing bucket, so the mount fails instead of retrying; a domain-based DFS namespace ends there too, because go-smb2 follows no DFS referrals. A WebDAV 401 counts the same as a refused session: servers that take Basic auth against a directory count every refused request as a failed login, and rclone's WebDAV backend answers it with `*api.Error` (`StatusCode` 401) without retrying.
- `fs.Option.Sensitive` is broader than "secret": for SFTP it covers `host` and `user`, for S3 the access keys, `session_token` and the SSE keys. `IsPassword` is set only for real passwords (`pass`, `key_file_pass`); no S3 option has it. Lagra therefore treats every sensitive option as a secret, lets an account list identifiers as `public`, and never lets a password be public.
- `NewFs` never runs a backend's `Config` function. Backends that log in there cannot be opened from option values alone: OAuth in the browser (Google Drive, OneDrive, Dropbox, Box, pCloud, Google Cloud Storage without a service account, ...) and login steps that fetch and store tokens or keys (iCloud Drive with a 2FA code, Internxt, PikPak, SugarSync). Proton Drive logs in inside `NewFs` (with `otp_secret_key` even under 2FA) but stores its session through `m.Set` to reuse it; Lagra keeps nothing rclone writes back, so every connect would be a fresh login.
- Supporting those needs a sign-in flow: run the backend's config state machine from the daemon, render its questions in the settings window, receive the browser callback, store the token in the keychain and write refreshed tokens and sessions back (Box and OneDrive rotate refresh tokens).
- A wrong S3 secret shows up as `SignatureDoesNotMatch` (403) on the backend probe's listing; the mount stays offline until the secret is fixed.

## go-nfs

- Up to v0.0.4, the latest tag, a connection's requests are handled strictly one after another. macOS sends everything for a volume over one connection, so a single slow request (a folder with thousands of entries, a READ waiting for a download) held up all others, the mount probe included: a busy volume looked broken, and the remount cancelled the running download.
- The master branch since 2026-09-26 (`v0.0.5-0.20260926204150-6274d051a57f`) has `Server.ConcurrentHandlers`: request bodies are read into memory and handled in parallel, replies go out by XID. rclone v1.75.1 builds against it unchanged. Lagra pins that commit, serves NFSv3 only (`EnabledNFSVersions`) and allows 16 requests per connection. Move to a tag once go-nfs releases one.
- A new connection gets its own goroutine, so the NFS NULL procedure on a separate connection answers even while the volume's connection is busy. Lagra uses that as the server probe, to tell a busy volume from a dead server.
- go-nfs answers CREATE in EXCLUSIVE mode (what macOS sends for `open(O_CREAT|O_EXCL)`, i.e. every file Finder copies) with NOTSUPP and logs an error; macOS then retries without O_EXCL. For a `._` sidecar the retry meets the Apple filter, which go-nfs logs as "Error Creating". Both are normal and came in pairs for every copied file, so Lagra logs them at debug (`rclone.RouteLogs`).
- NFSv4 in go-nfs is NFSv4.0 without named attributes (OPENATTR), grace period or reclaim, and with numeric owner strings. macOS stores extended attributes natively only through named attributes, so v4 would still need `._` files; Lagra stays on v3.

## Writing over NFS

- Writes need `vfs_cache_mode = writes` or `full`; otherwise the NFS export is read-only.
- Uploads start after `vfs_write_back` (default 5 s), run asynchronously and are retried forever with backoff capped at 5 minutes. The writing application never sees an upload error, which is why Lagra shows pending uploads instead. Files still waiting survive a restart with the same cache directory.
- rclone's filters (`--exclude`) only hide entries from listings; they do not block writes. That is why Lagra refuses Apple litter in the NFS layer.
- S3 has no directories: an empty folder created in Finder disappears unless `directory_markers = true`, which Lagra sets for S3 by default.

## macOS

- rclone's own `nfsmount` runs `mount -o port=P,mountport=P,tcp localhost:/ <mp>` without sudo, a **hard** mount: a dead backend hangs every caller.
- Lagra mounts with grrclone's set instead: `soft,intr,timeo=600,retrans=2,nolocks,locallocks,nfc,rsize=131072,wsize=131072` (`sysmount.nfsOptions`). With `soft`, macOS gives up on a request and returns "Operation timed out" instead of hanging. `timeo × (retrans+1)` = 3 minutes is only the upper bound: with the storage frozen, a read failed after about 8 s and a folder listing after about 37 s (presumably because macOS's retransmit timer adapts to the round-trip time, which is tiny on `127.0.0.1`). A folder of 40,000 entries listed through a 1 MB/s link failed the same way after about 40 s. Lagra itself waits the full 3 minutes before it calls a busy volume hung, so it never remounts while macOS is still waiting.
- `mount_nfs` returns only after the server answered FSSTAT. rclone's NFS handler takes the free space from `vfs.Statfs`, which asks the storage (SMB can tell, S3 cannot) under a lock and waits for the connection: with an SMB server that does not answer (VPN down) up to rclone's connect timeout of a minute, longer than the 30 s Lagra gives `mount_nfs`, so the mount was killed and retried forever. Lagra's handler answers FSSTAT within 2 s with the last numbers it has (at first "unknown", as for S3) and asks the storage once at a time in the background. rclone's SMB backend also connects in `NewFs` to check whether a path with a folder is a file, unless the path ends in a slash; Lagra appends one, since it only mounts folders. A storage that cannot be reached now comes up as an offline volume, as S3 always did.
- The mount probe looks up a random name in the root, so that macOS cannot answer from its attribute cache. go-nfs turns that into `Lstat`, which rclone's VFS answers by listing the root on the storage. Lagra's filter answers names starting with `.lagra-probe-` itself, so the probe tells whether the volume answers; the backend probe asks the storage.
- `--noappledouble` and `--noapplexattr` exist only for FUSE mounts; they do nothing for the NFS server.
- NFSv3 has no extended attributes, so Finder writes a `._name` AppleDouble sidecar for every copied file and drops `.DS_Store`. The preference `com.apple.desktopservices DSDontWriteNetworkStores` suppresses only `.DS_Store`; Lagra's Apple filter refuses both, and the copy itself succeeds. Finder ignores the refusal; `cp` reports "could not copy extended attributes … Operation not permitted" and exits with 1 although the file arrives (`cp -X` skips the attributes).
- The mount table lists real paths (`/private/tmp/x` for `/tmp/x`). Lagra resolves symlinks in the directories above a mount point before it mounts, or it would not find its own volume in the table, take it for ejected and stop its server under a live mount.
- `/Volumes` is owned by root; mount points under `$HOME` need no privileges.
- Unmount: `diskutil unmount <mp>`, then `diskutil unmount force <mp>`.
