# The TLS module the palm-gw app loads: the wolfSSL 5.9.4 client
# (tls_client.c, GPLv3 with wolfSSL) behind the step buffer of tlsmod.h,
# built position-independent for ../loader (docs/PALM-APP.md). From
# CradleServer's palm/tls/Makefile.
#
#   make           build build/module/*.bin, the module's resources
#                  (armc/armd/armh from ID 2000), which ../app puts into
#                  PalmGW.prc
#   make host-test build the client for this machine and run it against a
#                  TLS 1.3 server set up as palm-gw's (./probe): first use,
#                  the right pin, a wrong pin
#   make clean

PALM_PREFIX ?= $(HOME)/.local/tessera-palm
T = $(PALM_PREFIX)/bin/
WOLFSSL = ../third_party/wolfssl
LOADER = ../loader
B = build
MODULE_ID = 2000

# The wolfSSL sources the client needs.
WOLFSSL_SRC = $(addprefix $(WOLFSSL)/src/,internal.c keys.c ssl.c tls.c tls13.c wolfio.c) \
	$(addprefix $(WOLFSSL)/wolfcrypt/src/,aes.c asn.c chacha.c chacha20_poly1305.c poly1305.c \
	curve25519.c fe_low_mem.c fe_operations.c ge_low_mem.c ge_operations.c ed25519.c ecc.c \
	sp_c32.c sp_int.c sha256.c sha512.c hash.c hmac.c kdf.c random.c memory.c wc_port.c \
	wolfmath.c error.c logging.c coding.c signature.c wc_encrypt.c)

# ARMv4T for the T|X's ARM926 and the E2's ARM925; -fPIC with r10 as the
# PIC base keeps every address out of the code (../loader).
ARM_CFLAGS = -O2 -march=armv4t -fPIC -msingle-pic-base -mpic-register=r10 -ffreestanding -fno-builtin \
	-std=gnu99 -ffunction-sections -fdata-sections -Wall -Wno-unused-function \
	-DWOLFSSL_USER_SETTINGS -I. -I../include -I$(WOLFSSL)
ARM_OBJS = $(addprefix $(B)/arm/,$(notdir $(WOLFSSL_SRC:.c=.o)) tls_client.o palm_libc.o module.o)
ARM_LINK = $(T)arm-palmos-gcc $(ARM_CFLAGS) -nostdlib -nostartfiles -Wl,-T,$(LOADER)/module.ld -Wl,--gc-sections

vpath %.c . $(WOLFSSL)/src $(WOLFSSL)/wolfcrypt/src

all: $(B)/module/armh07d0.bin

$(B)/arm/%.o: %.c user_settings.h tls_client.h tlsmod.h
	@mkdir -p $(B)/arm
	$(T)arm-palmos-gcc $(ARM_CFLAGS) -c -o $@ $<

# cmd/palmlink compares four links (reference, code moved, data moved, and
# a check with both moved) to find the words the loader relocates, then
# writes the module's resources.
LINKS = ref:0x0:0x1000000 code:0x100000:0x1000000 data:0x0:0x1200000 check:0x300000:0x2400000

$(B)/module-%.elf: $(ARM_OBJS) $(LOADER)/entry.S $(LOADER)/module.ld
	$(ARM_LINK) -Wl,-Ttext,$(word 2,$(subst :, ,$(filter $*:%,$(LINKS)))) \
		-Wl,-Tdata,$(word 3,$(subst :, ,$(filter $*:%,$(LINKS)))) -o $@ $(LOADER)/entry.S $(ARM_OBJS) -lgcc

$(B)/module/armh07d0.bin: $(foreach l,$(LINKS),$(B)/module-$(firstword $(subst :, ,$(l))).elf)
	rm -rf $(B)/module && mkdir -p $(B)/module
	cd ../.. && go run ./cmd/palmlink -out palm-app/tls/$(B)/module -id $(MODULE_ID) \
		$(foreach l,$(LINKS),-link palm-app/tls/$(B)/module-$(subst :,.elf:,$(word 1,$(subst :, ,$(l))):)$(subst $(word 1,$(subst :, ,$(l))):,,$(l)))
	@$(T)arm-palmos-size $(B)/module-ref.elf | tail -1 | awk '{ print "module: code " $$1 " bytes, data " $$2 ", bss " $$3 }'

HOST_CFLAGS = -O2 -Wall -Wno-unused-function -DWOLFSSL_USER_SETTINGS -I. -I$(WOLFSSL)

$(B)/host_test: host_test.c tls_client.c tls_client.h tls_heap.c user_settings.h $(WOLFSSL_SRC)
	mkdir -p $(B)
	$(CC) $(HOST_CFLAGS) -o $@ host_test.c tls_client.c $(WOLFSSL_SRC)

host-test: $(B)/host_test
	cd ../.. && go run ./palm-app/tls/probe palm-app/tls/$(B)/host_test

clean:
	rm -rf $(B)

.PHONY: all host-test clean

# Keep the objects: they are intermediates of the four links, and make
# would otherwise delete them and compile wolfSSL again every time.
.SECONDARY:
