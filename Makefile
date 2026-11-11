# palm-gw build and install targets (PLAN.md section 8.11).
MODULE   := palm-gw
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS  := -trimpath
LDFLAGS  := -s -w -X main.version=$(VERSION)
BIN      := bin
DIST     := dist
CMDS     := ./cmd/...

# The release tag at HEAD, if any (v1.2.3, v0.6.0-rc1).
RELEASE_TAG ?= $(shell git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null)
# .deb version: the tag on a tagged commit (v1.2.3 -> 1.2.3; v0.6.0-rc1 -> 0.6.0~rc1, so a
# pre-release sorts before its release), otherwise a snapshot that sorts by commit time
# (0.0.0~git20261111071500.c2e8b5c), so each build upgrades the last.
DEB_VERSION ?= $(shell v='$(RELEASE_TAG)'; \
	if [ -n "$$v" ]; then echo "$${v\#v}" | tr - '~'; \
	else echo "0.0.0~git$$(git log -1 --format=%cd --date=format:%Y%m%d%H%M%S).$$(git rev-parse --short HEAD)"; fi)
NFPM     ?= go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0

# make install options (passed through to scripts/install.sh)
ADAPTER  ?=
UPLINK   ?=
CONFIG   ?=

.PHONY: all build build-arm64 test vet lint fmt install uninstall deb image release publish support-bundle clean

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

# Release artefacts (D-044), from a clean tree at a v* tag only, into dist/release/:
#   palm-gw_<tag>_arm64.deb   the package (named after the tag: no ~ in a download URL)
#   install.sh                the bootstrap installer with the tag baked in
#   SHA256SUMS                for both
# `make publish` uploads them; `make release` never touches GitHub.
REL      := $(DIST)/release
REL_DEB  := palm-gw_$(patsubst v%,%,$(RELEASE_TAG))_arm64.deb
SHA256   ?= $(shell command -v sha256sum >/dev/null && echo sha256sum || echo 'shasum -a 256')

release:
	@test -n "$(RELEASE_TAG)" || { echo "make release: HEAD is not at a v* tag (git tag -a vX.Y.Z)" >&2; exit 2; }
	@test -z "$$(git status --porcelain)" || { echo "make release: the tree has uncommitted changes" >&2; exit 2; }
	$(MAKE) deb
	mkdir -p $(REL)
	cp $(DIST)/palm-gw_$(DEB_VERSION)_arm64.deb $(REL)/$(REL_DEB)
	sed 's/@RELEASE_TAG@/$(RELEASE_TAG)/' packaging/bootstrap-install.sh > $(REL)/install.sh
	chmod 0755 $(REL)/install.sh
	cd $(REL) && $(SHA256) $(REL_DEB) install.sh > SHA256SUMS
	@cat $(REL)/SHA256SUMS

# The ready-to-flash image (D-002, D-045): the pinned Raspberry Pi OS Lite (64-bit) image with
# the .deb installed, built by packaging/build-image.sh in a privileged arm64 Docker container
# (on a Linux arm64 host, run the script directly as root). The base is downloaded once into
# dist/base/ and checked against PIOS_SHA256. Writes dist/palm-gw-<ver>-rpi-arm64.img.xz, its
# .sha256, and a Raspberry Pi Imager repository file (.json).
PIOS_URL    ?= https://downloads.raspberrypi.com/raspios_lite_arm64/images/raspios_lite_arm64-2026-10-06/2026-10-06-raspios-trixie-arm64-lite.img.xz
PIOS_SHA256 ?= 483db18a48da399b5b7022ffae9a07bc6d99daab30e593aedac09b69ff422843
PIOS_IMG    := $(DIST)/base/$(notdir $(PIOS_URL))
IMG_VERSION := $(if $(RELEASE_TAG),$(patsubst v%,%,$(RELEASE_TAG)),$(subst ~,-,$(DEB_VERSION)))
IMAGE       := $(DIST)/palm-gw-$(IMG_VERSION)-rpi-arm64.img.xz
IMAGE_URL   ?= $(if $(RELEASE_TAG),https://github.com/lasergirl-ca/pi-blueap/releases/download/$(RELEASE_TAG)/$(notdir $(IMAGE)),$(notdir $(IMAGE)))
DOCKER      ?= docker

$(PIOS_IMG):
	mkdir -p $(DIST)/base
	curl -fL --retry 3 -o $@.part '$(PIOS_URL)'
	echo '$(PIOS_SHA256)  $@.part' | $(SHA256) -c -
	mv $@.part $@

image: deb $(PIOS_IMG)
	$(DOCKER) run --rm --privileged --platform linux/arm64 -v '$(CURDIR)':/src -w /src \
		-e WORK=/work -e DEBIAN_FRONTEND=noninteractive -e IMAGE_VERSION='$(DEB_VERSION)' -e IMAGE_URL='$(IMAGE_URL)' debian:trixie-slim \
		bash -c 'apt-get update -qq && apt-get install -y -qq fdisk e2fsprogs xz-utils zerofree >/dev/null && \
			packaging/build-image.sh "$(PIOS_IMG)" "$(DIST)/palm-gw_$(DEB_VERSION)_arm64.deb" "$(IMAGE)"'
	@ls -l $(IMAGE) $(IMAGE).sha256 $(IMAGE:.img.xz=.json)

# Pushes the tag, creates the GitHub release and uploads the artefacts. A tag with a
# hyphen (v0.6.0-rc1) is a pre-release, which releases/latest skips.
publish: release
	git push origin $(RELEASE_TAG)
	gh release create $(RELEASE_TAG) --verify-tag --title "palm-gw $(RELEASE_TAG)" --generate-notes \
		$(if $(findstring -,$(RELEASE_TAG)),--prerelease) \
		$(REL)/$(REL_DEB) $(REL)/install.sh $(REL)/SHA256SUMS

support-bundle:
	@echo "make support-bundle: arrives in Phase 8" >&2; exit 2

clean:
	rm -rf $(BIN) $(DIST)
