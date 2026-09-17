package notestore

import (
	"bytes"
	"compress/gzip"
	"io"
	"unicode/utf16"
)

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
	FontHints      int
	FontWeight     int
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

// Decode gunzips and parses a ZDATA blob.
func Decode(blob []byte) (*Note, error) {
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
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
		return &Note{}, nil
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
		return &Note{}, nil
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
			r.Length = int(f.val)
		case 2:
			if f.typ == wireBytes {
				ps, err := decodeParagraphStyle(f.data)
				if err != nil {
					return err
				}
				r.ParagraphStyle = ps
			}
		case 3:
			if f.typ == wireBytes {
				r.FontName, r.FontHints = decodeFont(f.data)
			}
		case 5:
			r.FontWeight = int(f.val)
		case 6:
			r.Underlined = f.val != 0
		case 7:
			r.Strikethrough = f.val != 0
		case 8:
			r.Superscript = int(int32(f.val))
		case 9:
			if f.typ == wireBytes {
				r.Link = string(f.data)
			}
		case 12:
			if f.typ == wireBytes {
				a, err := decodeAttachment(f.data)
				if err != nil {
					return err
				}
				r.Attachment = a
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
			ps.StyleType = int(int32(f.val))
		case 2:
			ps.Alignment = int(f.val)
		case 4:
			ps.IndentAmount = int(f.val)
		case 5:
			if f.typ == wireBytes {
				c, err := decodeChecklist(f.data)
				if err != nil {
					return err
				}
				ps.Checklist = c
			}
		case 8:
			ps.BlockQuote = int(f.val)
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
			c.UUID = append([]byte(nil), f.data...)
		case 2:
			c.Done = f.val != 0
		}
		return nil
	})
	return c, err
}

// decodeFont returns the font name and its hint bits. Notes encodes italic and
// bold in the hints rather than in dedicated fields.
func decodeFont(b []byte) (string, int) {
	var name string
	var hints int
	_ = scan(b, func(f field) error {
		switch f.num {
		case 1:
			if f.typ == wireBytes {
				name = string(f.data)
			}
		case 3:
			hints = int(f.val)
		}
		return nil
	})
	return name, hints
}

func decodeAttachment(b []byte) (*AttachmentInfo, error) {
	a := &AttachmentInfo{}
	err := scan(b, func(f field) error {
		switch f.num {
		case 1:
			a.Identifier = string(f.data)
		case 2:
			a.TypeUTI = string(f.data)
		}
		return nil
	})
	return a, err
}

// units returns the note text as UTF-16 code units, which is the unit
// AttributeRun.Length is expressed in.
func (n *Note) units() []uint16 { return utf16.Encode([]rune(n.Text)) }
