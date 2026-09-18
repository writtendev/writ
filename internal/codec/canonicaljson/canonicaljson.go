// Package canonicaljson encodes arbitrary JSON as byte-stable canonical
// JSON: the same logical value always produces the same bytes, which is
// what op signing and content-addressing need. The normative definition
// of the encoding is spec/canonicalization.md; this package is the
// reference implementation. See
// docs/decisions/0001-canonical-json-encoding.md for why this is a small
// bespoke encoder rather than an RFC 8785 library.
package canonicaljson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Marshal parses src as a single JSON value and re-encodes it in
// canonical form: object members sorted by UTF-16 code unit order of
// their keys, numbers formatted per the ECMAScript Number::toString
// algorithm, and strings re-escaped with the minimal required escapes.
//
// Per spec/canonicalization.md, three classes of input are rejected
// rather than silently normalized, because Go's decoder would otherwise
// erase them before the encoder could see them and a differently-built
// verifier could disagree on the bytes:
//
//   - input that is not valid UTF-8 (encoding/json substitutes U+FFFD),
//   - objects with duplicate member keys (the map decode keeps the last),
//   - lone (unpaired) UTF-16 surrogate escapes in strings (decoded to
//     U+FFFD, indistinguishable afterwards from a legal literal U+FFFD).
//
// Numbers are decoded as IEEE 754 double-precision floats, so integers
// beyond 2^53 lose precision exactly as they would in JavaScript or any
// other double-based JSON consumer — see spec/canonicalization.md for
// why op payload fields that need exact large integers must carry them
// as strings instead.
func Marshal(src []byte) ([]byte, error) {
	if !utf8.Valid(src) {
		return nil, fmt.Errorf("canonicaljson: input is not valid UTF-8")
	}
	// Lone surrogates are only detectable in the raw text: by the time
	// encoding/json hands us a string they are already U+FFFD.
	if err := checkSurrogateEscapes(src); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	v, err := decodeValue(dec, 0)
	if err != nil {
		return nil, err
	}
	// dec.More() cannot serve as the trailing-data check: it peeks one
	// byte and reports false for '}' and ']', so `{"a":1}}` would look
	// like clean EOF. Asking for one more token distinguishes real EOF
	// from any trailing byte.
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("canonicaljson: trailing data after JSON value")
	}
	var buf bytes.Buffer
	buf.Grow(len(src))
	if err := encodeValue(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// maxNestingDepth caps decodeValue's recursion at the spec'd 10000
// levels (spec/canonicalization.md): the 10000th open bracket is
// accepted, the 10001st rejected, exactly matching what encoding/json
// enforces inside Decode — so a payload this package accepts is never
// one json.Unmarshal chokes on. Decoder.Token, which the walk below
// uses instead of Decode, enforces no limit of its own, and without a
// cap a long run of `[` bytes drives one stack frame per byte until the
// process dies.
const maxNestingDepth = 10000

// decodeValue builds the value tree token by token rather than through
// json.Decoder.Decode into map[string]any, because the map decode
// collapses duplicate object keys (last value wins) before they can be
// detected. Walking tokens lets each object reject a repeated key the
// moment it appears.
func decodeValue(dec *json.Decoder, depth int) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("canonicaljson: %w", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		// nil, bool, string, or json.Number — already the final value.
		// Scalars are not depth-checked: only containers nest, and
		// encoding/json counts brackets the same way, so checking here
		// too would reject a leaf inside 10000 brackets that
		// json.Unmarshal accepts.
		return tok, nil
	}
	// depth is the number of enclosing brackets, so this container is
	// the (depth+1)th. The 10000th is the last one accepted.
	if depth >= maxNestingDepth {
		return nil, fmt.Errorf("canonicaljson: exceeded max nesting depth of %d", maxNestingDepth)
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("canonicaljson: %w", err)
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, fmt.Errorf("canonicaljson: object key is %T, not string", keyTok)
			}
			if _, dup := obj[key]; dup {
				return nil, fmt.Errorf("canonicaljson: duplicate object key %q", key)
			}
			val, err := decodeValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			obj[key] = val
		}
		if _, err := dec.Token(); err != nil { // consume '}'
			return nil, fmt.Errorf("canonicaljson: %w", err)
		}
		return obj, nil
	case '[':
		arr := []any{}
		for dec.More() {
			val, err := decodeValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		if _, err := dec.Token(); err != nil { // consume ']'
			return nil, fmt.Errorf("canonicaljson: %w", err)
		}
		return arr, nil
	default:
		return nil, fmt.Errorf("canonicaljson: unexpected delimiter %v", delim)
	}
}

// checkSurrogateEscapes scans the raw JSON text for \uXXXX escapes in
// the surrogate range (U+D800–U+DFFF) that do not form a high+low pair,
// and rejects them. It tracks just enough string structure to find
// escapes; a malformed escape is skipped rather than trusted, so the
// scan keeps checking the rest of the input and the decoder (which runs
// next) reports the syntax error.
func checkSurrogateEscapes(src []byte) error {
	inString := false
	for i := 0; i < len(src); i++ {
		if !inString {
			if src[i] == '"' {
				inString = true
			}
			continue
		}
		switch src[i] {
		case '"':
			inString = false
		case '\\':
			if i+1 >= len(src) {
				return nil // truncated input; decoder reports the syntax error
			}
			if src[i+1] != 'u' {
				i++ // skip the escaped character (covers \\ and \")
				continue
			}
			u, ok := hex4(src[i+2:])
			if !ok {
				i++ // malformed \u escape; decoder rejects it — keep scanning
				continue
			}
			i += 5 // now at the escape's last hex digit
			if !utf16.IsSurrogate(rune(u)) {
				continue
			}
			u2, ok2 := uint16(0), false
			if i+2 < len(src) && src[i+1] == '\\' && src[i+2] == 'u' {
				u2, ok2 = hex4(src[i+3:])
			}
			if ok2 && utf16.DecodeRune(rune(u), rune(u2)) != unicode.ReplacementChar {
				i += 6 // a valid high+low pair; consume the low half
				continue
			}
			return fmt.Errorf(`canonicaljson: lone surrogate \u%04x in string`, u)
		}
	}
	return nil
}

// hex4 parses exactly four hex digits from the front of b.
func hex4(b []byte) (uint16, bool) {
	if len(b) < 4 {
		return 0, false
	}
	u, err := strconv.ParseUint(string(b[:4]), 16, 16)
	if err != nil {
		return 0, false
	}
	return uint16(u), true
}

func encodeValue(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		return encodeNumber(buf, t)
	case string:
		encodeString(buf, t)
	case []any:
		return encodeArray(buf, t)
	case map[string]any:
		return encodeObject(buf, t)
	default:
		return fmt.Errorf("canonicaljson: unsupported value type %T", v)
	}
	return nil
}

func encodeArray(buf *bytes.Buffer, arr []any) error {
	buf.WriteByte('[')
	for i, elem := range arr {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := encodeValue(buf, elem); err != nil {
			return err
		}
	}
	buf.WriteByte(']')
	return nil
}

type objectKey struct {
	str   string
	utf16 []uint16
}

func encodeObject(buf *bytes.Buffer, obj map[string]any) error {
	keys := make([]objectKey, 0, len(obj))
	for k := range obj {
		keys = append(keys, objectKey{str: k, utf16: utf16.Encode([]rune(k))})
	}
	sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i].utf16, keys[j].utf16) })

	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodeString(buf, k.str)
		buf.WriteByte(':')
		if err := encodeValue(buf, obj[k.str]); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// lessUTF16 orders by UTF-16 code unit sequence (RFC 8785 §3.2.3), which
// is not the same as Go's native UTF-8 byte order once a key contains a
// character outside the Basic Multilingual Plane: UTF-16 encodes those
// as a surrogate pair in the D800-DFFF range, which sorts before U+E000
// even though the character it represents is above U+FFFF. See the
// "supplementary-plane vs BMP" test vector. Callers encode each key's
// UTF-16 form once up front rather than on every comparison.
func lessUTF16(ua, ub []uint16) bool {
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// encodeString ranges over s as decoded runes. Lone (unpaired) UTF-16
// surrogates never reach it: Marshal rejects them in the raw input
// (checkSurrogateEscapes) before decoding, as spec/canonicalization.md
// requires, so a U+FFFD here is always a literal U+FFFD from the source
// and passes through as such.
func encodeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}

func encodeNumber(buf *bytes.Buffer, n json.Number) error {
	f, err := n.Float64()
	if err != nil {
		return fmt.Errorf("canonicaljson: invalid number %q: %w", n, err)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("canonicaljson: %v is not representable in JSON", f)
	}
	buf.WriteString(formatNumber(f))
	return nil
}

// formatNumber implements the ECMAScript Number::toString algorithm
// (the encoding RFC 8785 mandates), the shortest decimal string that
// round-trips to f, switching to exponential notation outside
// [1e-6, 1e21) the same way JavaScript's Number.prototype.toString
// does. strconv.AppendFloat with the 'e' verb and precision -1 supplies
// the shortest round-trip digit string; this reformats it to match.
func formatNumber(f float64) string {
	if f == 0 {
		// Also collapses -0 to "0", which RFC 8785 requires.
		return "0"
	}
	neg := f < 0
	if neg {
		f = -f
	}

	digits, exp := shortestDigits(f)
	k := len(digits)
	n := exp + 1 // position of the decimal point within digits, 1-based

	var out string
	switch {
	case k <= n && n <= 21:
		out = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		out = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		out = "0." + strings.Repeat("0", -n) + digits
	default:
		mantissa := digits[:1]
		if k > 1 {
			mantissa += "." + digits[1:]
		}
		e := n - 1
		sign := "+"
		if e < 0 {
			sign = "-"
			e = -e
		}
		out = mantissa + "e" + sign + strconv.Itoa(e)
	}
	if neg {
		return "-" + out
	}
	return out
}

// shortestDigits returns the shortest round-trip significant-digit
// string for f (no leading/trailing zeros beyond a lone "0", f > 0) and
// exp such that digits * 10^(exp-len(digits)+1) == f.
func shortestDigits(f float64) (digits string, exp int) {
	sci := strconv.AppendFloat(nil, f, 'e', -1, 64)
	s := string(sci)
	eIdx := strings.IndexByte(s, 'e')
	mantissa := strings.Replace(s[:eIdx], ".", "", 1)
	exp, _ = strconv.Atoi(s[eIdx+1:])
	return mantissa, exp
}
