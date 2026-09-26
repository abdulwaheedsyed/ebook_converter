# Static, cross-compiled builds. CGO is off, so no C toolchain is needed for
# any target. Only 64-bit x86 and ARM are listed: those are the architectures
# where the WebAssembly runtime compiles to native code. Elsewhere it falls
# back to an interpreter and becomes far too slow to be useful.

BINARY    := leafbind
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

export CGO_ENABLED := 0

.PHONY: build test test-short dist package app winres notices clean

# Windows builds carry the icon and version information as resources, which
# tools/packaging writes as .syso objects that go build links in.
winres:
	go run ./tools/packaging winres -version $(VERSION)

build: winres
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .
	@rm -f rsrc_windows_*.syso

test:
	go test ./...

test-short:
	go test -short ./...

dist: winres
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; \
		[ $$os = windows ] && ext=.exe; \
		out=dist/$(BINARY)-$(VERSION)-$$os-$$arch$$ext; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$out . || exit 1; \
	done
	@rm -f rsrc_windows_*.syso
	@cp LICENSE THIRD_PARTY_NOTICES.md dist/

# A macOS application bundle for this Mac, to try it out: dist/Leafbind.app.
app:
	@mkdir -p dist
	go build -trimpath -ldflags "$(LDFLAGS)" -o dist/leafbind-app .
	@rm -rf dist/Leafbind.app
	go run ./tools/packaging app -version $(VERSION) -bin dist/leafbind-app -o dist/Leafbind.app
	@rm dist/leafbind-app

# Release archives: tar.gz for Linux and macOS, which keeps the executable
# bit, zip for Windows. Each holds the binary, LICENSE, the third-party
# notices and the README. On macOS the binary is inside Leafbind.app, with
# leafbind beside it as a link for the command line; on Linux a desktop
# entry and icons come with it. Uses sha256sum, so run it on Linux (as CI
# does). The macOS bundles are signed afterwards, on a Mac.
package: dist
	@rm -rf dist/pkg
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; \
		[ $$os = windows ] && ext=.exe; \
		name=$(BINARY)-$(VERSION)-$$os-$$arch; dir=dist/pkg/$$name; \
		mkdir -p $$dir; \
		if [ $$os = darwin ]; then \
			go run ./tools/packaging app -version $(VERSION) -bin dist/$$name -o $$dir/Leafbind.app >/dev/null || exit 1; \
			ln -s Leafbind.app/Contents/MacOS/$(BINARY) $$dir/$(BINARY); \
		else \
			cp dist/$$name$$ext $$dir/$(BINARY)$$ext; \
		fi; \
		if [ $$os = linux ]; then go run ./tools/packaging linux -o $$dir || exit 1; fi; \
		cp LICENSE THIRD_PARTY_NOTICES.md README.md $$dir/; \
		if [ $$os = windows ]; then \
			(cd dist/pkg && zip -qr ../$$name.zip $$name); \
		else \
			tar -C dist/pkg --owner=0 --group=0 --numeric-owner -czf dist/$$name.tar.gz $$name; \
		fi; \
		echo "packaged $$name"; \
	done
	@rm -rf dist/pkg
	@cd dist && sha256sum *.tar.gz *.zip > SHA256SUMS

# Regenerate after changing dependencies; the tests fail until you do.
notices:
	go run ./tools/gennotices

clean:
	rm -rf dist $(BINARY) $(BINARY).exe rsrc_windows_*.syso
