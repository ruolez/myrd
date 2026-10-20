# Builds librcnative.a for the Linux/X11 demo-device backend.
CC      ?= cc
CFLAGS  ?= -O2 -g -std=gnu11 -Wall -Wextra -Wno-unused-parameter -fPIC
CFLAGS  += -I../include
BUILD   := build

$(BUILD)/librcnative.a: $(BUILD)/rc_linux.o
	ar rcs $@ $^

$(BUILD)/rc_linux.o: rc_linux.c ../include/rc_native.h | $(BUILD)
	$(CC) $(CFLAGS) -c rc_linux.c -o $@

$(BUILD):
	mkdir -p $(BUILD)

clean:
	rm -rf $(BUILD)

.PHONY: clean
