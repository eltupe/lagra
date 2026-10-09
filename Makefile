# lagra — build, bundle, sign, install, release
APP        := lagra
APPNAME    := Lagra
BUNDLE_ID  := io.github.eltupe.lagra
# Versions come from git tags such as v0.1.0. VERSION is what `lagra version`
# prints: the tag without its v, between releases followed by the commits
# since the tag, the commit and -dirty. The bundle needs plain numbers:
# SHORT_VERSION is the last tag, BUILD_NUMBER the commit count.
VERSION       ?= $(or $(shell git describe --tags --always --dirty --match 'v[0-9]*' 2>/dev/null | sed 's/^v//'),dev)
SHORT_VERSION ?= $(or $(shell git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null | sed -E 's/^v//; s/[^0-9.].*//'),0.0.0)
BUILD_NUMBER  ?= $(or $(shell git rev-list --count HEAD 2>/dev/null),0)
BIN        := bin/$(APP)
APPDIR     := build/$(APPNAME).app
LDFLAGS    := -s -w -X main.version=$(VERSION)
# The oldest macOS Lagra runs on; Info.plist and binary must agree. Without
# -mmacosx-version-min, clang targets the macOS it runs on and the binary
# refuses to start on anything older.
MACOS_MIN  := 14.0
# ARCH is arm64 or amd64. cgo is off by default in cross builds, and its clang
# must target the same architecture.
ARCH       ?= $(shell go env GOARCH)
CLANG_ARCH := $(if $(filter amd64,$(ARCH)),x86_64,arm64)
GOENV      := GOARCH=$(ARCH) CGO_ENABLED=1 CC="clang -arch $(CLANG_ARCH)" \
              CGO_CFLAGS="-O2 -g -mmacosx-version-min=$(MACOS_MIN)" \
              CGO_LDFLAGS="-mmacosx-version-min=$(MACOS_MIN)"
# /Applications is writable for admin users without sudo; non-admin users
# install with INSTALLDIR=$HOME/Applications.
INSTALLDIR ?= /Applications
LINKDIR    ?= $(HOME)/.local/bin
# Signing identity: a local self-signed "lagra Developer" certificate keeps the
# app's identity stable across builds, so macOS privacy grants (Files and
# Folders, Network Volumes) survive rebuilds. Falls back to ad-hoc ("-").
SIGN_ID    ?= $(shell security find-identity -v -p codesigning 2>/dev/null | grep -q '"lagra Developer"' && echo "lagra Developer" || echo "-")

.PHONY: all build test vet tidy audit app sign cert install uninstall dist dist-arch clean run
# App icon for Finder and Launchpad: the menu bar anchor in white, no shadow,
# on a vertical gradient #2f78ad → #1a4f7f. macOS icon grid: 1024 units, the
# body 824 wide and centred. The body must be a rounded square with corner
# radius 185.4: macOS 26 puts any other shape (a superellipse included) into
# a grey frame.
ICON       := docs/images/AppIcon.icns

all: build

build:
	@mkdir -p $(dir $(BIN))
	$(GOENV) go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/lagra

vet:
	go vet ./...

test:
	go test -race ./...

# Tidy go.mod, then format and modernize the code.
tidy:
	go mod tidy
	go fix ./...
	go fmt ./...

# Checks before a commit or release. staticcheck and govulncheck are tool
# dependencies in go.mod, so everyone runs the same versions.
audit:
	go mod tidy -diff
	go mod verify
	go vet ./...
	go tool staticcheck ./...
	go tool govulncheck ./...
	go test -race -vet=off ./...

# .app bundle: LSUIElement hides the Dock icon; the same binary is the CLI.
app: build $(ICON)
	@rm -rf $(APPDIR)
	@mkdir -p $(APPDIR)/Contents/MacOS $(APPDIR)/Contents/Resources
	@cp $(BIN) $(APPDIR)/Contents/MacOS/$(APP)
	@cp $(ICON) $(APPDIR)/Contents/Resources/AppIcon.icns
	@# Binaries must carry their licenses: Lagra's own and those of every
	@# module linked into it (see hack/notices).
	@cp LICENSE $(APPDIR)/Contents/Resources/LICENSE
	@go run ./hack/notices > $(APPDIR)/Contents/Resources/THIRD_PARTY_NOTICES.txt
	@printf '%s\n' \
	'<?xml version="1.0" encoding="UTF-8"?>' \
	'<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
	'<plist version="1.0"><dict>' \
	'  <key>CFBundleName</key><string>$(APPNAME)</string>' \
	'  <key>CFBundleDisplayName</key><string>$(APPNAME)</string>' \
	'  <key>CFBundleIdentifier</key><string>$(BUNDLE_ID)</string>' \
	'  <key>CFBundleVersion</key><string>$(BUILD_NUMBER)</string>' \
	'  <key>CFBundleShortVersionString</key><string>$(SHORT_VERSION)</string>' \
	'  <key>NSHumanReadableCopyright</key><string>Copyright © 2026 Michael Schlegel, GPL-3.0-or-later</string>' \
	'  <key>CFBundleExecutable</key><string>$(APP)</string>' \
	'  <key>CFBundlePackageType</key><string>APPL</string>' \
	'  <key>CFBundleIconFile</key><string>AppIcon</string>' \
	'  <key>LSUIElement</key><true/>' \
	'  <key>LSMinimumSystemVersion</key><string>$(MACOS_MIN)</string>' \
	'  <key>NSHighResolutionCapable</key><true/>' \
	'</dict></plist>' > $(APPDIR)/Contents/Info.plist
	@# The same binary is the CLI and the app: started from the bundle without
	@# arguments it runs `lagra app` itself (see cmd/lagra/main.go).
	@echo built $(APPDIR)

# Sign with the stable local identity when present (see SIGN_ID), else ad hoc.
# SIGN_FLAGS adds codesign options; dist sets the hardened runtime.
sign: app
	codesign --force --sign "$(SIGN_ID)" --identifier $(BUNDLE_ID) $(SIGN_FLAGS) $(APPDIR)
	codesign --verify --deep --strict $(APPDIR) && echo "signed with $(SIGN_ID)"

# Local signing identity "lagra Developer". Without Apple's paid Developer ID,
# a self-signed certificate is the standard way to give a locally built app a
# stable identity: macOS privacy grants (Files and Folders, Network Volumes)
# are bound to that identity and survive rebuilds. Two steps need your login
# password, once: trusting the certificate for code signing (a dialog) and
# letting codesign use its key (a prompt in the terminal). OpenSSL 3 needs
# -legacy for a .p12 that `security import` reads; macOS's own LibreSSL
# writes that format anyway and does not know the flag.
KEYCHAIN := $(HOME)/Library/Keychains/login.keychain-db
cert:
	@security find-certificate -c "lagra Developer" $(KEYCHAIN) >/dev/null 2>&1 || ( \
	  echo "Creating the local signing certificate 'lagra Developer'…" && \
	  T=$$(mktemp -d) && cd $$T && \
	  openssl req -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes -keyout key.pem -out cert.pem \
	    -subj "/CN=lagra Developer/O=lagra/OU=local" -addext "keyUsage=critical,digitalSignature" \
	    -addext "extendedKeyUsage=critical,codeSigning" -addext "basicConstraints=critical,CA:false" 2>/dev/null && \
	  LEGACY=$$(openssl pkcs12 -help 2>&1 | grep -q -- -legacy && echo -legacy || true) && \
	  openssl pkcs12 -export $$LEGACY -out lagra.p12 -inkey key.pem -in cert.pem -name "lagra Developer" -passout pass:lagra-temp && \
	  security import lagra.p12 -k $(KEYCHAIN) -P lagra-temp -T /usr/bin/codesign -T /usr/bin/security >/dev/null && \
	  rm -rf $$T )
	@security find-identity -v -p codesigning 2>/dev/null | grep -q '"lagra Developer"' || ( \
	  echo "" && echo "macOS needs your login password once to trust the certificate for code signing and to let codesign use its key." && \
	  mkdir -p build && security find-certificate -c "lagra Developer" -p $(KEYCHAIN) > build/lagra-developer.pem && \
	  security add-trusted-cert -r trustRoot -p codeSign -k $(KEYCHAIN) build/lagra-developer.pem && \
	  security set-key-partition-list -S apple-tool:,apple:,codesign: -s $(KEYCHAIN) >/dev/null )
	@security find-identity -v -p codesigning 2>/dev/null | grep -q '"lagra Developer"' && echo "signing identity: lagra Developer" || echo "warning: no trusted identity; falling back to ad-hoc signing (privacy prompts will repeat after rebuilds)"

install: cert
	@$(MAKE) --no-print-directory sign
	@mkdir -p $(INSTALLDIR) $(LINKDIR)
	@rm -rf $(INSTALLDIR)/$(APPNAME).app
	@cp -R $(APPDIR) $(INSTALLDIR)/
	@ln -sfn $(INSTALLDIR)/$(APPNAME).app/Contents/MacOS/$(APP) $(LINKDIR)/$(APP)
	@echo "installed $(INSTALLDIR)/$(APPNAME).app; CLI at $(LINKDIR)/$(APP) (add $(LINKDIR) to PATH)"
	@echo "start with: open $(INSTALLDIR)/$(APPNAME).app   (or set launch_at_login = true)"

uninstall:
	-$(LINKDIR)/$(APP) quit --force 2>/dev/null
	-launchctl bootout gui/$$(id -u)/$(BUNDLE_ID).agent 2>/dev/null
	rm -f $(HOME)/Library/LaunchAgents/$(BUNDLE_ID).agent.plist $(LINKDIR)/$(APP)
	rm -rf $(INSTALLDIR)/$(APPNAME).app
	@echo "removed app, CLI link and LaunchAgent; config, cache, logs and keychain items are untouched"

# Release archives for both architectures: dist/Lagra-<version>-<arch>.zip and
# dist/SHA256SUMS. Without DEVELOPER_ID they are signed ad hoc, good for
# testing; Gatekeeper blocks them on other Macs. A release sets
# DEVELOPER_ID="Developer ID Application: <name> (<team id>)" and
# NOTARY_PROFILE, a profile saved with `xcrun notarytool store-credentials`.
# The hardened runtime is on in both cases: notarization requires it.
# docs/releasing.md has the whole procedure.
DIST_ARCHS     := arm64 amd64
DEVELOPER_ID   ?= -
NOTARY_PROFILE ?=

dist:
	@rm -rf dist && mkdir -p dist
	@for arch in $(DIST_ARCHS); do \
	  $(MAKE) --no-print-directory dist-arch ARCH=$$arch BIN=build/$$arch/$(APP) APPDIR=build/$$arch/$(APPNAME).app || exit 1; \
	done
	@cd dist && shasum -a 256 *.zip > SHA256SUMS && cat SHA256SUMS

# One architecture of dist. ditto keeps the bundle's signature and metadata,
# which zip does not.
dist-arch: SIGN_ID = $(DEVELOPER_ID)
dist-arch: SIGN_FLAGS = --options runtime $(if $(filter-out -,$(DEVELOPER_ID)),--timestamp)
dist-arch: sign
	@if [ -n "$(NOTARY_PROFILE)" ]; then \
	  ditto -c -k --keepParent $(APPDIR) build/$(ARCH)/notarize.zip && \
	  xcrun notarytool submit build/$(ARCH)/notarize.zip --keychain-profile "$(NOTARY_PROFILE)" --wait && \
	  xcrun stapler staple $(APPDIR) || exit 1; \
	else \
	  echo "not notarized (no NOTARY_PROFILE): Gatekeeper will block this build on other Macs"; \
	fi
	ditto -c -k --keepParent $(APPDIR) dist/$(APPNAME)-$(VERSION)-$(CLANG_ARCH).zip

run: build
	$(BIN) run

clean:
	rm -rf bin build dist
