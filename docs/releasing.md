# Releasing

How a version of Lagra gets from a commit to a download.

## Versions

A release is a git tag `vMAJOR.MINOR.PATCH`. The Makefile derives everything from it:

- `VERSION`, what `lagra version` and the log print: `0.1.0` on the tag, `0.1.0-3-g1a2b3c4-dirty` between releases, the bare commit before the first tag.
- `SHORT_VERSION`, the bundle's `CFBundleShortVersionString`: the numbers of the last tag, `0.0.0` before the first.
- `BUILD_NUMBER`, the bundle's `CFBundleVersion`: the number of commits, so it only grows.

Any of them can be set on the command line, for example `make dist VERSION=0.1.0`.

## Licenses

Every bundle carries `LICENSE` and `THIRD_PARTY_NOTICES.txt` in `Contents/Resources`. The notices are generated at build time by `hack/notices` from the modules actually linked into the binary, so they follow `go.mod` without anyone keeping a list. It fails when a module has no license file at its root: look up that module's license by hand and teach `hack/notices` where to find it. The GPL's offer of source code is the tagged commit on GitHub, which the notices point to.

## One-time setup

1. Join the Apple Developer Program and create a "Developer ID Application" certificate (Xcode → Settings → Accounts → Manage Certificates). `security find-identity -v -p codesigning` then lists it as `Developer ID Application: <name> (<team id>)`.
2. Store credentials for the notary service in the keychain, under a profile name of your choice:

   ```sh
   xcrun notarytool store-credentials lagra --apple-id <apple id> --team-id <team id>
   ```

   It asks for an app-specific password (appleid.apple.com → Sign-In and Security).

## Cutting a release

1. Move the changes under a new heading in `CHANGELOG.md`, with the date, and commit.
2. Tag and build:

   ```sh
   git tag -a v0.1.0 -m "Lagra 0.1.0"
   make test
   make dist DEVELOPER_ID="Developer ID Application: <name> (<team id>)" NOTARY_PROFILE=lagra
   ```

   `dist/` then holds `Lagra-0.1.0-arm64.zip`, `Lagra-0.1.0-x86_64.zip` and `SHA256SUMS`. Both apps are signed with the hardened runtime, notarized and stapled, so Gatekeeper lets them open, offline too.
3. Check before publishing:

   ```sh
   spctl --assess --type execute -vv build/arm64/Lagra.app   # accepted, source=Notarized Developer ID
   unzip -q dist/Lagra-0.1.0-arm64.zip -d /tmp/lagra-check && /tmp/lagra-check/Lagra.app/Contents/MacOS/lagra version
   ```

   Then quit the installed Lagra, replace it with the new build and try it for real: menu bar icon, settings window, a volume connecting, `osascript -e 'quit app "Lagra"'` unmounting everything.
4. Push the tag (`git push origin v0.1.0`), create a GitHub release for it with the changelog section as notes, and attach the two zips and `SHA256SUMS`.

Without `DEVELOPER_ID` and `NOTARY_PROFILE`, `make dist` signs ad hoc. Those archives are fine for testing on your own Mac; on any other, Gatekeeper refuses to open them.
