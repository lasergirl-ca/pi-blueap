# palm-gw build and install targets (PLAN.md section 8.11).
MODULE   := palm-gw
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS  := -trimpath
LDFLAGS  := -s -w -X main.version=$(VERSION)
BIN      := bin
DIST     := dist
CMDS     := ./cmd/...

# .deb version: the tag (v1.2.3 -> 1.2.3) on a tagged commit, otherwise a snapshot that
# sorts by commit time (0.0.0~git20261111071500.c2e8b5c), so each build upgrades the last.
DEB_VERSION ?= $(shell v=$$(git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null); \
	if [ -n "$$v" ]; then echo "$${v\#v}"; \
	else echo "0.0.0~git$$(git log -1 --format=%cd --date=format:%Y%m%d%H%M%S).$$(git rev-parse --short HEAD)"; fi)
NFPM     ?= go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0

# make install options (passed through to scripts/install.sh)
ADAPTER  ?=
UPLINK   ?=
CONFIG   ?=

.PHONY: all build build-arm64 test vet lint fmt install uninstall deb release support-bundle clean

all: build

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)/ $(CMDS)

build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/linux-arm64/ $(CMDS)

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -l -w .

# Must run as root on the Pi; refuses to run on anything but Linux/arm64.
install:
	ADAPTER=$(ADAPTER) UPLINK=$(UPLINK) CONFIG=$(CONFIG) VERSION=$(VERSION) scripts/install.sh

uninstall:
	scripts/uninstall.sh

# The .deb (D-043): arm64 binaries, the static files rendered for the deb layout (D-042)
# into dist/stage (rewritten in place each time; the file set is fixed), then nfpm. Runs on
# the development machine.
deb: build-arm64
	go run ./cmd/palm-gw setup --install-files --layout deb --root $(DIST)/stage
	DEB_VERSION='$(DEB_VERSION)' $(NFPM) package -f packaging/nfpm.yaml -p deb -t $(DIST)/
	@ls -l $(DIST)/*.deb

release:
	@echo "make release: arrives in Phase 6" >&2; exit 2

support-bundle:
	@echo "make support-bundle: arrives in Phase 8" >&2; exit 2

clean:
	rm -rf $(BIN) $(DIST)
