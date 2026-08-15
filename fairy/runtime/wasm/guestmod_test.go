package wasm

func appendULEB(dst []byte, v uint32) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			dst = append(dst, c|0x80)
			continue
		}
		return append(dst, c)
	}
}

func section(id byte, payload []byte) []byte {
	out := []byte{id}
	out = appendULEB(out, uint32(len(payload)))
	return append(out, payload...)
}

func vec(items ...[]byte) []byte {
	out := appendULEB(nil, uint32(len(items)))
	for _, item := range items {
		out = append(out, item...)
	}
	return out
}

func name(value string) []byte {
	out := appendULEB(nil, uint32(len(value)))
	return append(out, value...)
}

func code(locals byte, body ...byte) []byte {
	payload := append([]byte{locals}, body...)
	out := appendULEB(nil, uint32(len(payload)))
	return append(out, payload...)
}

func abiGuest(handle []byte, minPages uint32) []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	module = append(module, section(1, vec(
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f},       // (i32)->i32 alloc
		[]byte{0x60, 0x02, 0x7f, 0x7f, 0x00},       // (i32,i32)->nil free
		[]byte{0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7e}, // (i32,i32)->i64 init/handle
		[]byte{0x60, 0x00, 0x00},                   // ()->nil shutdown
	))...)
	module = append(module, section(3, vec(
		[]byte{0x00}, []byte{0x01}, []byte{0x02}, []byte{0x02}, []byte{0x03},
	))...)
	memory := append([]byte{0x00}, appendULEB(nil, minPages)...)
	module = append(module, section(5, vec(memory))...)
	module = append(module, section(6, vec([]byte{0x7f, 0x01, 0x41, 0x80, 0x08, 0x0b}))...) // mut i32=1024
	module = append(module, section(7, vec(
		append(name("memory"), 0x02, 0x00),
		append(name("fairy_alloc"), 0x00, 0x00),
		append(name("fairy_free"), 0x00, 0x01),
		append(name("fairy_init"), 0x00, 0x02),
		append(name("fairy_handle"), 0x00, 0x03),
		append(name("fairy_shutdown"), 0x00, 0x04),
	))...)
	alloc := code(1, 0x01, 0x7f,
		0x23, 0x00,
		0x21, 0x01,
		0x20, 0x01,
		0x20, 0x00,
		0x6a,
		0x24, 0x00,
		0x20, 0x01,
		0x0b,
	)
	free := code(0, 0x0b)
	init := code(0, 0x42, 0x00, 0x0b)
	shutdown := code(0, 0x0b)
	module = append(module, section(10, vec(alloc, free, init, handle, shutdown))...)
	return module
}

func echoHandle() []byte {
	// (local dst i32) (local i i32)
	return code(1, 0x02, 0x7f,
		0x20, 0x01, // len
		0x10, 0x00, // call alloc
		0x21, 0x02, // dst
		0x03, 0x40, // loop
		0x20, 0x03, // i
		0x20, 0x01, // len
		0x49,       // i32.lt_u
		0x04, 0x40, // if
		0x20, 0x02, // dst
		0x20, 0x03, // i
		0x6a,       // add
		0x20, 0x00, // ptr
		0x20, 0x03, // i
		0x6a,             // add
		0x2d, 0x00, 0x00, // i32.load8_u
		0x3a, 0x00, 0x00, // i32.store8
		0x20, 0x03,
		0x41, 0x01,
		0x6a,
		0x21, 0x03,
		0x0c, 0x01, // br loop
		0x0b,       // end if
		0x0b,       // end loop
		0x20, 0x02, // dst
		0xad,       // i64.extend_i32_u
		0x42, 0x20, // i64.const 32
		0x86,       // i64.shl
		0x20, 0x01, // len
		0xad,
		0x84, // i64.or
		0x0b,
	)
}

func spinHandle() []byte {
	return code(0,
		0x03, 0x40,
		0x0c, 0x00,
		0x0b,
		0x00, // unreachable
		0x0b,
	)
}

func growHandle() []byte {
	return code(0,
		0x41, 0x01, // i32.const 1
		0x40, 0x00, // memory.grow 0
		0x1a,       // drop
		0x42, 0x00, // i64.const 0
		0x0b,
	)
}

func echoGuestWASM() []byte {
	return abiGuest(echoHandle(), 1)
}

func spinGuestWASM() []byte {
	return abiGuest(spinHandle(), 1)
}

func growGuestWASM() []byte {
	return abiGuest(growHandle(), 1)
}

func largeMemoryGuestWASM(pages uint32) []byte {
	return abiGuest(echoHandle(), pages)
}
