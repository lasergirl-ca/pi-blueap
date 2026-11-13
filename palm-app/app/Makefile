# palm-gw for Palm OS 5 (docs/PALM-APP.md): the 68k application and the
# TLS module (../tls) in one PalmGW.prc.
#
#   make        build build/PalmGW.prc (needs the Palm toolchain at
#               $(PALM_PREFIX): prc-tools for 68k and ARM, PilRC, SDK 5r3;
#               Tessera's scripts/setup.sh --palm installs it)
#   make test   the host tests of the modules without UI (needs only cc)
#   make icons  draw icons/*.bmp again from icons/logo.tiff (macOS: sips)
#   make clean

PALM_PREFIX ?= $(HOME)/.local/tessera-palm
T = $(PALM_PREFIX)/bin/
B = build
LOADER = ../loader
TLS = ../tls
STAMP := $(shell date +%m%d-%H%M)
CFLAGS68 = -O2 -Wall -I. -I$(LOADER) -DAPP_BUILD='"$(STAMP)"'
SRC = app.c gw.c kv.c http.c
OBJS = $(SRC:%.c=$(B)/%.o)
HDRS = ptypes.h gw.h kv.h http.h resources.h $(TLS)/tlsmod.h $(TLS)/tls_client.h $(LOADER)/loader.c $(LOADER)/loader.h

all: $(B)/PalmGW.prc

$(B)/%.o: %.c $(HDRS)
	@mkdir -p $(B)
	$(T)m68k-palmos-gcc $(CFLAGS68) -c $< -o $@

$(B)/app: $(OBJS)
	$(T)m68k-palmos-gcc -o $@ $(OBJS)
	@$(T)m68k-palmos-size $@ | awk 'NR == 2 { print "68k code: " $$1 " bytes (one code resource holds 32768), data " $$2 ", bss " $$3 }'

$(TLS)/build/module/armh07d0.bin:
	$(MAKE) -C $(TLS)

$(B)/PalmGW.prc: $(B)/app palmgw.rcp palmgw.def resources.h $(wildcard icons/*.bmp) $(TLS)/build/module/armh07d0.bin
	rm -f $(B)/*.bin
	cd $(B) && $(T)pilrc -q -I .. ../palmgw.rcp .
	cd $(B) && $(T)build-prc -o PalmGW.prc ../palmgw.def app *.bin ../$(TLS)/build/module/*.bin
	@ls -l $@

test:
	@mkdir -p $(B)/tests
	@fail=0; for t in gw kv http; do \
		$(CC) -std=c89 -Wall -o $(B)/tests/test_$$t tests/test_$$t.c $$t.c || exit 1; \
		$(B)/tests/test_$$t > $(B)/tests/$$t.txt || fail=1; \
		grep -c '^ok' $(B)/tests/$$t.txt | sed "s/^/$$t: /; s/$$/ ok/"; grep '^FAIL' $(B)/tests/$$t.txt; \
	done; exit $$fail

icons:
	python3 tools/mkicons.py icons/logo.tiff icons

clean:
	rm -rf $(B)

.PHONY: all test icons clean
