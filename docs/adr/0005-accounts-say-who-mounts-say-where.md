---
status: accepted
amends: 0004 (mounts may set backend options)
amended-by: 0006 (the backend type moved to the mount as well)
---
# Accounts say who, mounts say where

ADR 0004 put every backend option into the account, so the server was part of the login. That fits S3, where several buckets sit behind one endpoint and one key pair, but not a login that works on many servers: a directory account (SMB against Active Directory, SSH against LDAP or with one key) needed one account per server, each with its own copy of the password, and a password change meant updating every copy. Decision: an account is who you are (type, secrets, the options its mounts share, `public`); a mount says where, with its own `[mount.options]` on top of the account's.

## Rules

- Secrets live only in the account; keychain items stay `<account>/<option>`. A mount option that is also one of its account's secrets is an error.
- A mount's options are checked like the account's: known to the backend, never a password, sensitive ones only when the account lists them in `public`. `public` is the account's statement about its login and covers its mounts.
- Options rclone requires are checked per mount, against the account's and the mount's options and the account's secrets together. An account alone may be incomplete, such as an SMB login without a server.
- Validation does not decide which options belong where: any non-secret option may sit in either place, so lagra keeps no per-backend list of "location" options (ADR 0001). The settings window decides presentation: for SMB, SFTP and FTP the server (and port, TLS) sits on the storage page next to the share or folder and the account page asks only for the login; other kinds keep their curated fields on the account. Every storage can override any option the account may keep in the file, under Advanced.

## Considered options

- Sharing the password between per-server accounts (a secret source pointing at another account's keychain item): removes the duplicate, but keeps one account per server and adds a reference the window would have to show.
- A fixed split per backend (host, port, endpoint, region and url always on the mount): the strictest file, but a list per backend inside lagra, and wrong for logins bound to one server (WebDAV, most S3 setups), whose mounts would all repeat the address.
- `public` per mount: self-contained mounts, but the same `public = ["host"]` in every mount of a login.

## Consequences

- Config format, validation, settings service, settings window, example config and README change together. The engine, diffing and the keychain do not: an effective mount still carries the merged options.
- Editing a mount's options reconnects only that mount; editing the account reconnects all of its mounts, as before.
