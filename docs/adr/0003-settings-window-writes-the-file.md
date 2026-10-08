---
status: accepted
supersedes: the concept's "no settings window" and "lagra never writes the config file"
---
# A native settings window edits the config file in place

The original concept ruled out any settings UI and forbade lagra from writing its own config file. The user asked for a GUI that works alongside the dotfiles workflow. Decision: lagra ships a settings window (a WKWebView shell around a page the daemon serves on 127.0.0.1 with a per-process token) that edits the **same file**. The file stays the single source of truth: the window writes it, the hot-reload watcher applies it, exactly as if an editor had saved it.

## How the file is written

- Edits are surgical (`internal/tomledit`): a `[[mount]]` block is replaced, removed or appended as a whole; keys in `[general]`, `[defaults]` and `[defaults.vfs]` are edited individually and keep trailing comments. Everything outside the edited part is byte-identical. Comments **inside** a rewritten mount block are lost; this is documented.
- The result is validated with the real rclone metadata before anything is written. An invalid result is shown in the window and nothing is written.
- Writes go through symlinks (temp file + rename in the target's directory), so a dotfiles symlink is never replaced by a regular file.
- Secret values typed into the window go to the keychain; the file only gets `{ keychain = true }`. Renaming a mount moves its keychain items.

## Considered options

- Local page in the default browser: quickest, but the user wanted a native window.
- Wails: takes over the main run loop and conflicts with the systray integration; v3 was still alpha. A ~150-line Objective-C shell around WKWebView gives the same result with no extra framework.
- Fyne widgets: non-native look and an OpenGL dependency for what is essentially forms.

## Consequences

- The rule "the app never writes the file" is replaced by "the app only writes what the user changed, and only after validation". The original concept (`concept.md`) was kept as a historical note until 2026-10-08; it is in the git history.
- The web page is a plain embedded HTML/JS app; no frontend toolchain.
