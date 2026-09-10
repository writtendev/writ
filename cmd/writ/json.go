package main

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/internal/textsafe"
)

// emitJSON formats data into the versioned envelope and writes it as a single
// JSON document with a trailing newline.
//
// Before writing, it neutralises bidi and zero-width code points
// (spec/identifiers.md §Rendering a person identifier) with a single
// byte-level pass over the encoded document, rather than walking data
// looking for person-ref values: the envelope carries no schema, so this
// path has no reliable way to tell a person identifier from any other
// string it renders, and the same code points would still need catching
// inside a quarantined UnknownOp body, which is exactly where a hostile op
// lands. Escaping every string this envelope renders is safe as a byte-level
// pass, not just convenient: encoding/json already escapes everything below
// U+0020 itself within string content, and every other forbidden code point
// is either the single byte 0x7F or a multibyte UTF-8 sequence, neither of
// which can occur in JSON syntax outside string content. It is also
// lossless — a JSON decoder reads the \uXXXX escape back to the identical
// code point, so a machine consumer parsing this output sees no change from
// the unescaped value.
//
// The trailing newline json.Encoder.Encode appends is trimmed before the
// escape pass and re-appended afterward, unescaped: it is not JSON content
// at all, and textsafe.Forbidden's C0 range would otherwise misread that
// structural line feed as data and rewrite it into a spurious four-byte
// escape sequence nobody asked for.
func emitJSON(w io.Writer, kind string, data any) error {
	env := wire.Envelope{
		SchemaVersion: wire.CurrentSchemaVersion,
		Kind:          kind,
		Data:          data,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(env); err != nil {
		return err
	}
	doc := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if _, err := io.WriteString(w, textsafe.EscapeForbidden(string(doc))); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}
