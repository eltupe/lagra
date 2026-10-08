---
status: accepted
amends: 0003 (blocks the settings window edits)
amended-by: 0005 (mounts may set backend options; the server belongs to the mount), 0006 (the backend type belongs to the mount)
---
# Accounts hold the login, mounts hold the volume

Every mount used to carry its own backend type, options and secret sources, with keychain items named `<mount>/<option>`. Several buckets behind one S3 login needed the same keys stored once per mount, and copying a mount block in the dotfiles produced a mount whose keychain items did not exist. Decision: a new `[[account]]` block holds the login (type, backend options, secret sources, `public`); a mount names exactly one account and adds only what belongs to the Finder volume (path, mount point, lagra's switches, VFS options). Keychain items are named `<account>/<option>`.

## Rules

- A mount always goes through an account, so one place answers "how does lagra log in here". (ADR 0005 lets a mount add and override non-secret backend options, such as its server.)
- Validation copies the account's type, options and secret sources into each effective mount. Diffing, the engine and the probes stay unchanged: editing an account restarts exactly the mounts that use it.
- Accounts and mounts have separate name spaces. An unused account is valid.
- The settings window edits `[[account]]` blocks like mount blocks. Renaming an account moves its keychain items and points its mounts at the new name in one save; deleting it is refused while a mount uses it.
- No compatibility with the old mount format: lagra was never released. Old keys in a mount (`type`, `options`, `secrets`, `public`) are reported as "belongs to the account now".

## Considered options

- `keychain = "<other mount>"` references: fixes copying, but endpoint and region still repeat per mount and the reference chain is invisible in the UI.
- A separate `accounts.toml`: a second symlink and cross-file validation for no gain.
- rclone's own `rclone.conf` remotes: a standard format, but rclone keeps secrets in that file (only obscured), which breaks the rule that secrets live in the keychain or come from a command.

## Consequences

- Config format, settings service, web API, settings window and CLI (`lagra secret set|rm|check <account> …`) change together.
- The example config and the README describe accounts.
