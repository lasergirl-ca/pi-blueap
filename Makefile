# palm-gw build and install targets (PLAN.md section 8.11).
MODULE   := palm-gw
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS  := -trimpath
LDFLAGS  := -s -w -X main.version=$(VERSION)
BIN      := bin
DIST     := dist
CMDS     := ./cmd/...

# make install options (passed through to scripts/install.sh)
ADAPTER  ?= onboard
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

deb:
	@echo "make deb: nfpm packaging arrives in Phase 6" >&2; exit 2

release:
	@echo "make release: arrives in Phase 6" >&2; exit 2

support-bundle:
	@echo "make support-bundle: arrives in Phase 8" >&2; exit 2

clean:
	rm -rf $(BIN) $(DIST)
