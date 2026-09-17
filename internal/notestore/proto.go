package notestore

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Minimal protobuf wire-format reader. Apple's note bodies are a small, stable
// subset of proto2, so decoding them by hand keeps the build free of protoc and
// any generated code.

var (
	errTruncated = errors.New("notestore: truncated protobuf")
	errOverflow  = errors.New("notestore: varint overflows 64 bits")
)

type wireType uint8

const (
	wireVarint  wireType = 0
	wireFixed64 wireType = 1
	wireBytes   wireType = 2
	wireFixed32 wireType = 5
)

type field struct {
	num  int
	typ  wireType
	val  uint64 // varint / fixed
	data []byte // length-delimited
}

// scan walks every top-level field in buf, calling fn for each. Unknown fields
// are handed over too; callers ignore what they do not recognise, which is how
// this survives Apple adding fields in a new macOS release.
func scan(buf []byte, fn func(f field) error) error {
	for len(buf) > 0 {
		key, n := uvarint(buf)
		if n < 0 {
			return errOverflow
		}
		if n == 0 {
			return errTruncated
		}
		buf = buf[n:]
		f := field{num: int(key >> 3), typ: wireType(key & 7)}
		switch f.typ {
		case wireVarint:
			v, n := uvarint(buf)
			if n < 0 {
				return errOverflow
			}
			if n == 0 {
				return errTruncated
			}
			f.val, buf = v, buf[n:]
		case wireFixed64:
			if len(buf) < 8 {
				return errTruncated
			}
			f.val = binary.LittleEndian.Uint64(buf)
			buf = buf[8:]
		case wireFixed32:
			if len(buf) < 4 {
				return errTruncated
			}
			f.val = uint64(binary.LittleEndian.Uint32(buf))
			buf = buf[4:]
		case wireBytes:
			l, n := uvarint(buf)
			if n < 0 {
				return errOverflow
			}
			if n == 0 || uint64(len(buf)-n) < l {
				return errTruncated
			}
			f.data, buf = buf[n:n+int(l)], buf[n+int(l):]
		default:
			return fmt.Errorf("notestore: unsupported wire type %d", f.typ)
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return nil
}

// uvarint decodes a base-128 varint. It returns n > 0 on success, n == 0 when
// buf ran out mid-varint, and n < 0 when the value does not fit in 64 bits.
func uvarint(b []byte) (uint64, int) {
	var v uint64
	var s uint
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c < 0x80 {
			// At the tenth byte only one bit of headroom remains, so anything
			// above 1 overflows -- same rule encoding/binary applies.
			if i > 9 || (i == 9 && c > 1) {
				return 0, -1
			}
			return v | uint64(c)<<s, i + 1
		}
		v |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, 0
}
