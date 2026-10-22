# Builds librcnative.a for the Linux/X11 demo-device backend.
# VPX is a libvpx prefix (native/common/build-libvpx.sh); without it the
# system headers and library are used. Either way cgo links -lvpx from
# build/ first.
CC      ?= cc
CFLAGS  ?= -O2 -g -std=gnu11 -Wall -Wextra -Wno-unused-parameter -fPIC
CFLAGS  += -I../include -I../common $(if $(VPX),-I$(VPX)/include)
BUILD   := build

$(BUILD)/librcnative.a: $(BUILD)/rc_linux.o $(BUILD)/rc_vpx.o $(BUILD)/rc_scale.o
	ar rcs $@ $^
ifneq ($(VPX),)
	cp $(VPX)/lib/libvpx.a $(BUILD)/libvpx.a
endif

$(BUILD)/rc_linux.o: rc_linux.c ../include/rc_native.h ../common/rc_vpx.h ../common/rc_scale.h | $(BUILD)
	$(CC) $(CFLAGS) -c rc_linux.c -o $@

$(BUILD)/rc_vpx.o: ../common/rc_vpx.c ../common/rc_vpx.h ../include/rc_native.h | $(BUILD)
	$(CC) $(CFLAGS) -c ../common/rc_vpx.c -o $@

$(BUILD)/rc_scale.o: ../common/rc_scale.c ../common/rc_scale.h | $(BUILD)
	$(CC) $(CFLAGS) -c ../common/rc_scale.c -o $@

$(BUILD):
	mkdir -p $(BUILD)

clean:
	rm -rf $(BUILD)

.PHONY: clean
