package canonicaljson

import (
	"bytes"
	"encoding/json"
	"testing"
)

// encodeNumber's IsNaN/IsInf guard is unreachable through Marshal, since
// the JSON grammar has no NaN/Infinity literal and Float64 always errors
// on the numeric literals that would overflow to one. But strconv's
// ParseFloat also accepts the literal strings "NaN"/"Inf" and returns
// them without error, so a json.Number built by any means other than
// the decoder (e.g. a future caller of encodeNumber directly) can still
// reach the guard — exercise it directly so that path stays covered.
func TestEncodeNumberRejectsNaNAndInfDirectly(t *testing.T) {
	for _, s := range []string{"NaN", "Inf", "+Inf", "-Inf"} {
		var buf bytes.Buffer
		if err := encodeNumber(&buf, json.Number(s)); err == nil {
			t.Errorf("encodeNumber(%q) = %q, want error", s, buf.String())
		}
	}
}
