# Static, cross-compiled builds. CGO is off, so no C toolchain is needed for
# any target. Only 64-bit x86 and ARM are listed: those are the architectures
# where the WebAssembly runtime compiles to native code. Elsewhere it falls
# back to an interpreter and becomes far too slow to be useful.

BINARY    := ebook_converter
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

export CGO_ENABLED := 0

.PHONY: build test test-short dist clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

test-short:
	go test -short ./...

dist:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; \
		[ $$os = windows ] && ext=.exe; \
		out=dist/$(BINARY)-$(VERSION)-$$os-$$arch$$ext; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$out . || exit 1; \
	done

clean:
	rm -rf dist $(BINARY) $(BINARY).exe
