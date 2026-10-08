---
status: accepted
amends: 0004, 0005 (the backend type moves from the account to the mount)
---
# Accounts belong to no backend

After ADR 0005 an account still had a `type`, so a login used over two protocols needed two accounts with the same user and password, stored twice in the keychain. That is common: a directory account (Active Directory, LDAP) reaches SMB shares and WebDAV servers alike, and a service account wrote over both. Which protocol a volume uses is part of where it is, not of who you are. Decision: `type` moves to the mount. An account holds the login and options its mounts may share; each mount names its backend and takes the account's options and secrets that backend knows.

## Rules

- A mount requires `type`; an account must not have one ("type belongs to the mount").
- The account's options and secrets apply to a mount when the mount's backend has an option of that name (`user` and `pass` for SMB, WebDAV, SFTP and FTP alike, `domain` for SMB only). Values are checked against each mount's backend.
- Account validation looks options up across all offered backends (`Registry.Option`) with the strictest flags any of them sets: an option no backend has is an error, one that is a password anywhere must be a secret, one that is sensitive anywhere needs `public`.
- An option that none of an account's mounts can use is a warning: most likely a typo or a leftover.
- Keychain items stay `<account>/<option>`, so one password serves every protocol.
- The settings window picks the kind on the storage. The account page shows the fields of the kinds its storages use; for an account no storage uses yet, a chooser only decides which fields show. Saving keeps options and secrets of kinds not shown.

## Considered options

- Sharing a secret between accounts (`pass = { account = "x" }`): keeps one account per protocol and adds a reference the window has to explain; rejected in ADR 0005 already.
- An account bound to a set of types: no gain over deriving the set from its mounts.

## Consequences

- Config format, validation, settings service, settings window, example config and README change together. The engine and the keychain do not: an effective mount still carries its type, merged options and secret sources.
