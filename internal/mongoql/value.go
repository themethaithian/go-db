package mongoql

import "strings"

// Kind is what a parsed argument value is: one of the six JSON shapes, and
// nothing else. The grammar has no dates, no object ids and no regular
// expressions, because it has no way to evaluate them — MongoDB's extended
// JSON writes all three as ordinary objects ({"$oid": "…"}, {"$date": "…"}),
// which is what the driver reads them back as.
type Kind string

const (
	KindObject Kind = "object"
	KindArray  Kind = "array"
	KindString Kind = "string"
	KindNumber Kind = "number"
	KindBool   Kind = "bool"
	KindNull   Kind = "null"
)

// Field is one name-value pair of an object, in the order it was written.
// Order is kept because it is meaningful to MongoDB — an aggregation stage is
// an object with exactly one field, and a compound index or sort is read in
// order — and because losing it would make JSON render differently from what
// the human typed for no gain.
type Field struct {
	Name  string
	Value Value
}

// Value is one parsed argument, or one value inside one.
//
// It answers two different questions, because it has two consumers, and the
// split is deliberate:
//
//   - The classifier asks about structure. Is this pipeline an array? Is this
//     stage an object with exactly one field, and what is that field called?
//     Kind, Elements and Fields answer that, and nothing more is exported,
//     because nothing more can be asked without inspecting values the
//     classifier has no business judging.
//
//   - The MongoDB driver adapter asks for something it can send. JSON renders
//     the value as strict JSON, which is also relaxed extended JSON, which is
//     what bson.UnmarshalExtJSON reads. It is rendered from the parse rather
//     than sliced out of the input, so single quotes have become double,
//     unquoted keys have been quoted, and nothing the parser did not
//     understand can reach the driver in the bytes it unmarshals.
//
// The zero Value is a null.
type Value struct {
	kind     Kind
	text     string  // KindString: the decoded text. KindNumber: the literal as written.
	boolean  bool    // KindBool.
	fields   []Field // KindObject.
	elements []Value // KindArray.
}

// Kind reports which of the six shapes v is.
func (v Value) Kind() Kind {
	if v.kind == "" {
		return KindNull
	}
	return v.kind
}

// Fields returns an object's fields in the order they were written, and nil
// for every other kind. An object written {} has no fields, which is not the
// same as not being an object — ask Kind if the difference matters.
func (v Value) Fields() []Field { return v.fields }

// Elements returns an array's elements in order, and nil for every other kind.
func (v Value) Elements() []Value { return v.elements }

// JSON renders v as strict JSON: the form the MongoDB driver's extended-JSON
// unmarshaller reads, built from what the parser understood rather than from
// the bytes it was given.
func (v Value) JSON() string {
	var out strings.Builder
	v.writeJSON(&out)
	return out.String()
}

func (v Value) writeJSON(out *strings.Builder) {
	switch v.Kind() {
	case KindObject:
		out.WriteByte('{')
		for i, field := range v.fields {
			if i > 0 {
				out.WriteByte(',')
			}
			writeJSONString(out, field.Name)
			out.WriteByte(':')
			field.Value.writeJSON(out)
		}
		out.WriteByte('}')

	case KindArray:
		out.WriteByte('[')
		for i, element := range v.elements {
			if i > 0 {
				out.WriteByte(',')
			}
			element.writeJSON(out)
		}
		out.WriteByte(']')

	case KindString:
		writeJSONString(out, v.text)

	case KindNumber:
		// The literal as written. The number grammar is a subset of JSON's, so
		// what was accepted is already what JSON accepts, and re-formatting it
		// through a float would lose precision the driver is entitled to see.
		out.WriteString(v.text)

	case KindBool:
		if v.boolean {
			out.WriteString("true")
			return
		}
		out.WriteString("false")

	default:
		out.WriteString("null")
	}
}

// writeJSONString writes text as a JSON string literal. The parser has already
// decoded whatever escapes the input used and rejected anything that was not
// valid UTF-8, so this only has to escape what JSON requires.
func writeJSONString(out *strings.Builder, text string) {
	const hex = "0123456789abcdef"

	out.WriteByte('"')
	for i := 0; i < len(text); i++ {
		switch c := text[i]; c {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if c < 0x20 {
				out.WriteString(`\u00`)
				out.WriteByte(hex[c>>4])
				out.WriteByte(hex[c&0xf])
				continue
			}
			out.WriteByte(c)
		}
	}
	out.WriteByte('"')
}
