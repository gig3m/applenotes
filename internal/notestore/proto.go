package notestore

import (
	"errors"
	"fmt"
)

// Minimal protobuf wire-format reader. Apple's note bodies are a small, stable
// subset of proto2, so decoding them by hand keeps the build free of protoc and
// any generated code.

var errTruncated = errors.New("notestore: truncated protobuf")

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
		if n <= 0 {
			return errTruncated
		}
		buf = buf[n:]
		f := field{num: int(key >> 3), typ: wireType(key & 7)}
		switch f.typ {
		case wireVarint:
			v, n := uvarint(buf)
			if n <= 0 {
				return errTruncated
			}
			f.val, buf = v, buf[n:]
		case wireFixed64:
			if len(buf) < 8 {
				return errTruncated
			}
			buf = buf[8:]
		case wireFixed32:
			if len(buf) < 4 {
				return errTruncated
			}
			buf = buf[4:]
		case wireBytes:
			l, n := uvarint(buf)
			if n <= 0 || uint64(len(buf)-n) < l {
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

func uvarint(b []byte) (uint64, int) {
	var v uint64
	var s uint
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c < 0x80 {
			if i > 9 {
				return 0, -1
			}
			return v | uint64(c)<<s, i + 1
		}
		v |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, 0
}
