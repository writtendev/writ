package spec_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/writtendev/writ/internal/order"
	"github.com/writtendev/writ/spec"
)

const orderingSchemaID = "https://writ.dev/spec/ordering.schema.json"

func compileOrderingSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := spec.FS.ReadFile("schemas/ordering.schema.json")
	if err != nil {
		t.Fatalf("reading ordering schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoding ordering schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(orderingSchemaID, doc); err != nil {
		t.Fatalf("adding schema resource: %v", err)
	}
	sch, err := c.Compile(orderingSchemaID)
	if err != nil {
		t.Fatalf("compiling schema: %v", err)
	}
	return sch
}

type orderingVectors struct {
	Validation []struct {
		Key    string `json:"key"`
		Valid  bool   `json:"valid"`
		Reason string `json:"reason,omitempty"`
	} `json:"validation"`
	Generation []struct {
		Name     string `json:"name"`
		Before   string `json:"before"`
		After    string `json:"after"`
		Expected string `json:"expected"`
	} `json:"generation"`
	Comparison []struct {
		Name     string `json:"name"`
		PosA     string `json:"pos_a"`
		OpIDA    string `json:"op_id_a"`
		PosB     string `json:"pos_b"`
		OpIDB    string `json:"op_id_b"`
		Expected int    `json:"expected"`
	} `json:"comparison"`
}

func loadOrderingVectors(t *testing.T) orderingVectors {
	t.Helper()
	raw, err := spec.FS.ReadFile("testdata/ordering/vectors.json")
	if err != nil {
		t.Fatalf("reading testdata/ordering/vectors.json: %v", err)
	}
	var vecs orderingVectors
	if err := json.Unmarshal(raw, &vecs); err != nil {
		t.Fatalf("decoding testdata/ordering/vectors.json: %v", err)
	}
	return vecs
}

func TestOrdering_ValidationVectors(t *testing.T) {
	sch := compileOrderingSchema(t)
	vecs := loadOrderingVectors(t)

	for _, tc := range vecs.Validation {
		t.Run("key_"+tc.Key, func(t *testing.T) {
			valErr := order.Validate(tc.Key)
			schErr := sch.Validate(tc.Key)

			if tc.Valid {
				if valErr != nil {
					t.Errorf("order.Validate(%q) unexpected error: %v", tc.Key, valErr)
				}
				if schErr != nil {
					t.Errorf("schema validation(%q) unexpected error: %v", tc.Key, schErr)
				}
			} else {
				if valErr == nil {
					t.Errorf("order.Validate(%q) expected error for reason %q, got nil", tc.Key, tc.Reason)
				} else {
					switch tc.Reason {
					case "empty":
						if !errors.Is(valErr, order.ErrEmptyKey) {
							t.Errorf("order.Validate(%q) error %v does not match ErrEmptyKey", tc.Key, valErr)
						}
					case "trailing-zero":
						if !errors.Is(valErr, order.ErrTrailingZero) {
							t.Errorf("order.Validate(%q) error %v does not match ErrTrailingZero", tc.Key, valErr)
						}
					case "invalid-character":
						if !errors.Is(valErr, order.ErrInvalidCharacter) {
							t.Errorf("order.Validate(%q) error %v does not match ErrInvalidCharacter", tc.Key, valErr)
						}
					}
				}
				if schErr == nil {
					t.Errorf("schema validation(%q) expected error for reason %q, got nil", tc.Key, tc.Reason)
				}
			}
		})
	}
}

func TestOrdering_GenerationVectors(t *testing.T) {
	sch := compileOrderingSchema(t)
	vecs := loadOrderingVectors(t)

	for _, tc := range vecs.Generation {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := order.Between(tc.Before, tc.After)
			if err != nil {
				t.Fatalf("Between(%q, %q) unexpected error: %v", tc.Before, tc.After, err)
			}
			if got != tc.Expected {
				t.Fatalf("Between(%q, %q) = %q, want %q", tc.Before, tc.After, got, tc.Expected)
			}
			if err := order.Validate(got); err != nil {
				t.Fatalf("generated key %q failed Validate: %v", got, err)
			}
			if err := sch.Validate(got); err != nil {
				t.Fatalf("generated key %q failed schema validation: %v", got, err)
			}
			if tc.Before != "" && !(tc.Before < got) {
				t.Fatalf("expected %q < %q", tc.Before, got)
			}
			if tc.After != "" && !(got < tc.After) {
				t.Fatalf("expected %q < %q", got, tc.After)
			}
		})
	}
}

func TestOrdering_ComparisonVectors(t *testing.T) {
	vecs := loadOrderingVectors(t)

	for _, tc := range vecs.Comparison {
		t.Run(tc.Name, func(t *testing.T) {
			got := order.Compare(tc.PosA, tc.OpIDA, tc.PosB, tc.OpIDB)
			if got != tc.Expected {
				t.Fatalf("Compare(%q, %q, %q, %q) = %d, want %d",
					tc.PosA, tc.OpIDA, tc.PosB, tc.OpIDB, got, tc.Expected)
			}
			wantLess := tc.Expected < 0
			gotLess := order.Less(tc.PosA, tc.OpIDA, tc.PosB, tc.OpIDB)
			if gotLess != wantLess {
				t.Fatalf("Less(%q, %q, %q, %q) = %v, want %v",
					tc.PosA, tc.OpIDA, tc.PosB, tc.OpIDB, gotLess, wantLess)
			}
		})
	}
}
