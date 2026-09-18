package notestore

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math"
	"unicode/utf16"
)

var (
	errWireType = errors.New("unexpected wire type")
	// errMalformed marks a blob whose structure is not what the schema says.
	// Returning an empty note instead would be indistinguishable from an empty
	// note, and a rewrite would then truncate the real one.
	errMalformed = errors.New("malformed note")
)

// Policy on malformed fields: only AttributeRun.length is fatal, because
// reading it as zero silently misaligns every run that follows. Every other
// scalar, and every submessage below AttributeRun, costs at most a few
// attributes, so it is skipped rather than failing the whole note -- this is a
// read-only extraction tool, and a note that renders slightly wrong beats a
// note that will not open at all if Apple ever re-types a field.
//
// A structural failure in Document, Note or AttributeRun itself is still fatal:
// if a run body cannot be parsed, its length may be unread, and every offset
// after it would be wrong.

// Paragraph style types used by Notes. Anything unrecognised falls back to body
// text rather than being dropped.
const (
	StyleBody      = -1
	StyleTitle     = 0
	StyleHeading   = 1
	StyleSubhead   = 2
	StyleMonospace = 4
	StyleDotList   = 100
	StyleDashList  = 101
	StyleNumList   = 102
	StyleChecklist = 103
)

// Font style values carried in AttributeRun.font_weight. Despite the field
// name this is a style enum, not a weight -- confirmed against a real library.
const (
	FontDefault    = 0
	FontBold       = 1
	FontItalic     = 2
	FontBoldItalic = 3
)

type Checklist struct {
	UUID []byte
	Done bool
}

type ParagraphStyle struct {
	StyleType    int
	Alignment    int
	IndentAmount int
	Checklist    *Checklist
	BlockQuote   int
}

type AttachmentInfo struct {
	Identifier string
	TypeUTI    string
}

// AttributeRun styles a span of the note text. Length is counted in UTF-16 code
// units, matching NSAttributedString, not in bytes or runes.
type AttributeRun struct {
	Length         int
	ParagraphStyle *ParagraphStyle
	FontName       string
	PointSize      float32
	FontWeight     int // a FontDefault/FontBold/... style enum, not a weight
	Underlined     bool
	Strikethrough  bool
	Superscript    int
	Link           string
	Attachment     *AttachmentInfo
}

// Note is a decoded ZICNOTEDATA.ZDATA blob: one flat string plus the runs that
// style it.
type Note struct {
	Text string
	Runs []AttributeRun
}

// MaxDecompressed caps how far a ZDATA blob may expand. Real notes are far
// under this; the cap exists so a malformed or hostile blob cannot exhaust
// memory.
const MaxDecompressed = 256 << 20

// ErrTooLarge is returned when a blob decompresses past MaxDecompressed.
var ErrTooLarge = errors.New("notestore: decompressed note exceeds limit")

// Decode gunzips and parses a ZDATA blob.
func Decode(blob []byte) (*Note, error) {
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, MaxDecompressed+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxDecompressed {
		return nil, ErrTooLarge
	}
	return decodeNoteStore(raw)
}

func decodeNoteStore(raw []byte) (*Note, error) {
	var out *Note
	err := scan(raw, func(f field) error {
		if f.num == 2 && f.typ == wireBytes { // NoteStoreProto.document
			n, err := decodeDocument(f.data)
			if err != nil {
				return err
			}
			out = n
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("notestore: %w: no document", errMalformed)
	}
	return out, nil
}

func decodeDocument(b []byte) (*Note, error) {
	var out *Note
	err := scan(b, func(f field) error {
		if f.num == 3 && f.typ == wireBytes { // Document.note
			n, err := decodeNote(f.data)
			if err != nil {
				return err
			}
			out = n
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("notestore: %w: no note in document", errMalformed)
	}
	return out, nil
}

func decodeNote(b []byte) (*Note, error) {
	n := &Note{}
	err := scan(b, func(f field) error {
		switch {
		case f.num == 2 && f.typ == wireBytes: // note_text
			n.Text = string(f.data)
		case f.num == 5 && f.typ == wireBytes: // attribute_run
			r, err := decodeRun(f.data)
			if err != nil {
				return err
			}
			n.Runs = append(n.Runs, r)
		}
		return nil
	})
	return n, err
}

func decodeRun(b []byte) (AttributeRun, error) {
	r := AttributeRun{}
	err := scan(b, func(f field) error {
		switch f.num {
		case 1:
			if f.typ != wireVarint {
				return fmt.Errorf("notestore: AttributeRun.length: %w", errWireType)
			}
			// length is int32 in the schema; narrowing keeps a hostile varint
			// from producing a huge positive Length that overflows later
			// offset arithmetic.
			r.Length = int(int32(f.val))
		case 2:
			if f.typ == wireBytes {
				// A malformed style costs this run its formatting, not the note.
				if ps, err := decodeParagraphStyle(f.data); err == nil {
					r.ParagraphStyle = ps
				}
			}
		case 3:
			if f.typ == wireBytes {
				if name, size, err := decodeFont(f.data); err == nil {
					r.FontName, r.PointSize = name, size
				}
			}
		case 5:
			if f.typ != wireVarint {
				return nil
			}
			r.FontWeight = int(int32(f.val))
		case 6:
			if f.typ != wireVarint {
				return nil
			}
			r.Underlined = f.val != 0
		case 7:
			if f.typ != wireVarint {
				return nil
			}
			r.Strikethrough = f.val != 0
		case 8:
			if f.typ != wireVarint {
				return nil
			}
			r.Superscript = int(int32(f.val))
		case 9:
			if f.typ == wireBytes {
				r.Link = string(f.data)
			}
		case 12:
			if f.typ == wireBytes {
				if a, err := decodeAttachment(f.data); err == nil {
					r.Attachment = a
				}
			}
		}
		return nil
	})
	return r, err
}

func decodeParagraphStyle(b []byte) (*ParagraphStyle, error) {
	ps := &ParagraphStyle{StyleType: StyleBody}
	err := scan(b, func(f field) error {
		switch f.num {
		case 1:
			if f.typ != wireVarint {
				return nil
			}
			ps.StyleType = int(int32(f.val))
		case 2:
			if f.typ != wireVarint {
				return nil
			}
			ps.Alignment = int(int32(f.val))
		case 4:
			if f.typ != wireVarint {
				return nil
			}
			ps.IndentAmount = int(int32(f.val))
		case 5:
			if f.typ == wireBytes {
				if c, err := decodeChecklist(f.data); err == nil {
					ps.Checklist = c
				}
			}
		case 8:
			if f.typ != wireVarint {
				return nil
			}
			ps.BlockQuote = int(int32(f.val))
		}
		return nil
	})
	return ps, err
}

func decodeChecklist(b []byte) (*Checklist, error) {
	c := &Checklist{}
	err := scan(b, func(f field) error {
		switch f.num {
		case 1:
			if f.typ != wireBytes {
				return nil
			}
			c.UUID = append([]byte(nil), f.data...)
		case 2:
			if f.typ != wireVarint {
				return nil
			}
			c.Done = f.val != 0
		}
		return nil
	})
	return c, err
}

// decodeFont returns the font name and point size. font_hints is deliberately
// not read: Notes does not populate it, and bold/italic live in
// AttributeRun.font_weight.
func decodeFont(b []byte) (string, float32, error) {
	var name string
	var size float32
	err := scan(b, func(f field) error {
		switch f.num {
		case 1:
			if f.typ == wireBytes {
				name = string(f.data)
			}
		case 2:
			if f.typ == wireFixed32 {
				size = math.Float32frombits(uint32(f.val))
			}
		}
		return nil
	})
	return name, size, err
}

func decodeAttachment(b []byte) (*AttachmentInfo, error) {
	a := &AttachmentInfo{}
	err := scan(b, func(f field) error {
		switch f.num {
		case 1:
			if f.typ == wireBytes {
				a.Identifier = string(f.data)
			}
		case 2:
			if f.typ == wireBytes {
				a.TypeUTI = string(f.data)
			}
		}
		return nil
	})
	return a, err
}

// Bold reports whether the run is bold.
func (r AttributeRun) Bold() bool {
	return r.FontWeight == FontBold || r.FontWeight == FontBoldItalic
}

// Italic reports whether the run is italic.
func (r AttributeRun) Italic() bool {
	return r.FontWeight == FontItalic || r.FontWeight == FontBoldItalic
}

// units returns the note text as UTF-16 code units, which is the unit
// AttributeRun.Length is expressed in.
func (n *Note) units() []uint16 { return utf16.Encode([]rune(n.Text)) }
