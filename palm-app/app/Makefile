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
SRC = app.c gw.c kv.c http.c pair.c listui.c modes.c
OBJS = $(SRC:%.c=$(B)/%.o)
HDRS = ptypes.h gw.h kv.h http.h resources.h ui.h sections.h $(TLS)/tlsmod.h $(TLS)/tls_client.h $(LOADER)/loader.c $(LOADER)/loader.h

all: $(B)/PalmGW.prc

$(B)/%.o: %.c $(HDRS)
	@mkdir -p $(B)
	$(T)m68k-palmos-gcc $(CFLAGS68) -c $< -o $@

# Four code resources (palmgw.def, m68k-palmos-multigen): .text holds
# PilotMain, the connection, TLS and Status; pairf the Pair and Settings
# forms, listf the generic list, text and input forms, modef what each
# of them shows and does (sections.h). Each must stay under 32 KB, and
# tools/check-sections.sh fails the build on a call that crosses sections
# without the attribute (docs/PALM-DEV.md section 3).
$(B)/app: $(OBJS) palmgw.def
	cd $(B) && $(T)m68k-palmos-multigen -b app-sections ../palmgw.def
	$(T)m68k-palmos-gcc -o $@ $(OBJS) $(B)/app-sections.s $(B)/app-sections.ld
	@$(T)m68k-palmos-objdump -h $@ | awk 'function hex(h,  i, n) { n = 0; for (i = 1; i <= length(h); i++) n = n * 16 + index("0123456789abcdef", tolower(substr(h, i, 1))) - 1; return n } \
		$$2 ~ /^(\.text|pairf|listf|modef|\.data|\.bss)$$/ { n = hex($$3); print "  " $$2 ": " n " bytes" ((n > 32000 && $$2 != ".bss") ? "  TOO BIG" : ""); if (n > 32000 && $$2 != ".bss") bad = 1 } END { exit bad }'
	PATH=$(PALM_PREFIX)/bin:$$PATH sh tools/check-sections.sh $@ $(OBJS)

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
