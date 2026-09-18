package order_test

import (
	"errors"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/writtendev/writ/internal/order"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr error
	}{
		{"empty", "", order.ErrEmptyKey},
		{"single zero", "0", order.ErrTrailingZero},
		{"trailing zero after char", "a0", order.ErrTrailingZero},
		{"trailing zero after MidChar", "V0", order.ErrTrailingZero},
		{"multiple zeros", "00", order.ErrTrailingZero},
		{"middle zero with trailing zero", "010", order.ErrTrailingZero},
		{"hyphen", "a-b", order.ErrInvalidCharacter},
		{"space", "a b", order.ErrInvalidCharacter},
		{"underscore", "a_b", order.ErrInvalidCharacter},
		{"dot", "a.b", order.ErrInvalidCharacter},
		{"exclamation", "a!b", order.ErrInvalidCharacter},
		{"slash", "a/b", order.ErrInvalidCharacter},
		{"at sign", "@", order.ErrInvalidCharacter},
		{"null byte", "a\x00b", order.ErrInvalidCharacter},
		{"valid mid", "V", nil},
		{"valid min non-zero", "1", nil},
		{"valid lower", "a", nil},
		{"valid max", "z", nil},
		{"valid leading zero", "01", nil},
		{"valid leading zero mid", "0V", nil},
		{"valid multi char", "aV", nil},
		{"valid middle zero", "a0V", nil},
		{"valid middle zero min", "a01", nil},
		{"valid double z", "zzV", nil},
		{"valid triple zero", "0001", nil},
		{"valid mixed alphabet", "abcXYZ123", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := order.Validate(tc.key)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate(%q) unexpected error: %v", tc.key, err)
				}
			} else {
				if err == nil {
					t.Fatalf("Validate(%q) expected error %v, got nil", tc.key, tc.wantErr)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Validate(%q) expected error %v, got %v", tc.key, tc.wantErr, err)
				}
			}
		})
	}
}

func TestBetween_Boundaries(t *testing.T) {
	tests := []struct {
		name    string
		before  string
		after   string
		wantKey string
	}{
		{"empty collection", "", "", "V"},
		{"before V", "", "V", "F"},
		{"before F", "", "F", "7"},
		{"before 1", "", "1", "0V"},
		{"before 0V", "", "0V", "0F"},
		{"before 01", "", "01", "00V"},
		{"after V", "V", "", "k"},
		{"after k", "k", "", "s"},
		{"after z", "z", "", "zV"},
		{"after zV", "zV", "", "zk"},
		{"after zz", "zz", "", "zzV"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := order.Between(tc.before, tc.after)
			if err != nil {
				t.Fatalf("Between(%q, %q) unexpected error: %v", tc.before, tc.after, err)
			}
			if got != tc.wantKey {
				t.Fatalf("Between(%q, %q) = %q, want %q", tc.before, tc.after, got, tc.wantKey)
			}
			if err := order.Validate(got); err != nil {
				t.Fatalf("Between output %q is not canonical: %v", got, err)
			}
			if tc.before != "" && !(tc.before < got) {
				t.Fatalf("expected %q < %q", tc.before, got)
			}
			if tc.after != "" && !(got < tc.after) {
				t.Fatalf("expected %q < %q", got, tc.after)
			}
		})
	}
}

func TestBetween_Midpoints(t *testing.T) {
	tests := []struct {
		name    string
		before  string
		after   string
		wantKey string
	}{
		{"between a and c", "a", "c", "b"},
		{"between a and b", "a", "b", "aV"},
		{"between a and aV", "a", "aV", "aF"},
		{"between aF and aV", "aF", "aV", "aN"},
		{"between aV and b", "aV", "b", "ak"},
		{"between a and an", "a", "an", "aO"},
		{"between an and b", "an", "b", "at"},
		{"between a and a1", "a", "a1", "a0V"},
		{"between a0V and a1", "a0V", "a1", "a0k"},
		{"between az and b", "az", "b", "azV"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := order.Between(tc.before, tc.after)
			if err != nil {
				t.Fatalf("Between(%q, %q) unexpected error: %v", tc.before, tc.after, err)
			}
			if got != tc.wantKey {
				t.Fatalf("Between(%q, %q) = %q, want %q", tc.before, tc.after, got, tc.wantKey)
			}
			if err := order.Validate(got); err != nil {
				t.Fatalf("Between output %q is not canonical: %v", got, err)
			}
			if !(tc.before < got) {
				t.Fatalf("expected %q < %q", tc.before, got)
			}
			if !(got < tc.after) {
				t.Fatalf("expected %q < %q", got, tc.after)
			}
		})
	}
}

func TestBetween_InvalidInputs(t *testing.T) {
	tests := []struct {
		name      string
		before    string
		after     string
		errTarget error
	}{
		{"before > after", "b", "a", order.ErrInvalidRange},
		{"before == after", "a", "a", order.ErrInvalidRange},
		{"invalid before trailing zero", "a0", "b", order.ErrTrailingZero},
		{"invalid after trailing zero", "a", "b0", order.ErrTrailingZero},
		{"invalid before char", "a-", "b", order.ErrInvalidCharacter},
		{"invalid after char", "a", "b*", order.ErrInvalidCharacter},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := order.Between(tc.before, tc.after)
			if err == nil {
				t.Fatalf("Between(%q, %q) expected error %v, got key %q", tc.before, tc.after, tc.errTarget, got)
			}
			if !errors.Is(err, tc.errTarget) {
				t.Fatalf("Between(%q, %q) expected error %v, got %v", tc.before, tc.after, tc.errTarget, err)
			}
		})
	}
}

func TestBetween_RepeatedSubdivision(t *testing.T) {
	t.Run("insert at head 1000 times", func(t *testing.T) {
		current := "V"
		for i := 0; i < 1000; i++ {
			next, err := order.Between("", current)
			if err != nil {
				t.Fatalf("iteration %d: Between(\"\", %q) failed: %v", i, current, err)
			}
			if err := order.Validate(next); err != nil {
				t.Fatalf("iteration %d: key %q is not canonical: %v", i, next, err)
			}
			if !(next < current) {
				t.Fatalf("iteration %d: expected %q < %q", i, next, current)
			}
			current = next
		}
	})

	t.Run("insert at tail 1000 times", func(t *testing.T) {
		current := "V"
		for i := 0; i < 1000; i++ {
			next, err := order.Between(current, "")
			if err != nil {
				t.Fatalf("iteration %d: Between(%q, \"\") failed: %v", i, current, err)
			}
			if err := order.Validate(next); err != nil {
				t.Fatalf("iteration %d: key %q is not canonical: %v", i, next, err)
			}
			if !(current < next) {
				t.Fatalf("iteration %d: expected %q < %q", i, current, next)
			}
			current = next
		}
	})

	t.Run("insert between a and b 1000 times", func(t *testing.T) {
		before := "a"
		after := "b"
		for i := 0; i < 1000; i++ {
			mid, err := order.Between(before, after)
			if err != nil {
				t.Fatalf("iteration %d: Between(%q, %q) failed: %v", i, before, after, err)
			}
			if err := order.Validate(mid); err != nil {
				t.Fatalf("iteration %d: key %q is not canonical: %v", i, mid, err)
			}
			if !(before < mid && mid < after) {
				t.Fatalf("iteration %d: expected %q < %q < %q", i, before, mid, after)
			}
			// Subdivide toward before
			after = mid
		}
	})
}

func TestBetween_RandomPairsProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	randomCanonicalKey := func() string {
		n := rng.Intn(10) + 1
		b := make([]byte, n)
		for i := 0; i < n; i++ {
			b[i] = order.Alphabet[rng.Intn(len(order.Alphabet))]
		}
		// Ensure last char is not '0'
		for b[n-1] == '0' {
			b[n-1] = order.Alphabet[rng.Intn(len(order.Alphabet))]
		}
		return string(b)
	}

	for i := 0; i < 1000; i++ {
		k1 := randomCanonicalKey()
		k2 := randomCanonicalKey()
		if k1 == k2 {
			continue
		}
		var before, after string
		if k1 < k2 {
			before, after = k1, k2
		} else {
			before, after = k2, k1
		}

		mid, err := order.Between(before, after)
		if err != nil {
			t.Fatalf("pair %d: Between(%q, %q) failed: %v", i, before, after, err)
		}
		if err := order.Validate(mid); err != nil {
			t.Fatalf("pair %d: generated key %q is not canonical: %v", i, mid, err)
		}
		if !(before < mid && mid < after) {
			t.Fatalf("pair %d: expected %q < %q < %q", i, before, mid, after)
		}
	}
}

func TestCompareAndLess(t *testing.T) {
	type item struct {
		pos  string
		opID string
	}

	tests := []struct {
		name     string
		a        item
		b        item
		wantComp int
		wantLess bool
	}{
		{
			name:     "different position",
			a:        item{pos: "a", opID: "sha1"},
			b:        item{pos: "b", opID: "sha1"},
			wantComp: -1,
			wantLess: true,
		},
		{
			name:     "different position reversed",
			a:        item{pos: "b", opID: "sha1"},
			b:        item{pos: "a", opID: "sha1"},
			wantComp: 1,
			wantLess: false,
		},
		{
			name:     "same position, different opID",
			a:        item{pos: "aV", opID: "1111111111111111111111111111111111111111"},
			b:        item{pos: "aV", opID: "2222222222222222222222222222222222222222"},
			wantComp: -1,
			wantLess: true,
		},
		{
			name:     "same position, different opID reversed",
			a:        item{pos: "aV", opID: "2222222222222222222222222222222222222222"},
			b:        item{pos: "aV", opID: "1111111111111111111111111111111111111111"},
			wantComp: 1,
			wantLess: false,
		},
		{
			name:     "identical pos and opID",
			a:        item{pos: "aV", opID: "1111111111111111111111111111111111111111"},
			b:        item{pos: "aV", opID: "1111111111111111111111111111111111111111"},
			wantComp: 0,
			wantLess: false,
		},
		{
			name:     "position dominates opID",
			a:        item{pos: "a", opID: "ffffffffffffffffffffffffffffffffffffffff"},
			b:        item{pos: "b", opID: "0000000000000000000000000000000000000000"},
			wantComp: -1,
			wantLess: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotComp := order.Compare(tc.a.pos, tc.a.opID, tc.b.pos, tc.b.opID)
			if gotComp != tc.wantComp {
				t.Fatalf("Compare(%+v, %+v) = %d, want %d", tc.a, tc.b, gotComp, tc.wantComp)
			}
			gotLess := order.Less(tc.a.pos, tc.a.opID, tc.b.pos, tc.b.opID)
			if gotLess != tc.wantLess {
				t.Fatalf("Less(%+v, %+v) = %v, want %v", tc.a, tc.b, gotLess, tc.wantLess)
			}
		})
	}

	t.Run("sort.Slice integration", func(t *testing.T) {
		items := []item{
			{pos: "b", opID: "2222"},
			{pos: "a", opID: "4444"},
			{pos: "aV", opID: "3333"},
			{pos: "aV", opID: "1111"},
			{pos: "a", opID: "1111"},
		}

		sort.Slice(items, func(i, j int) bool {
			return order.Less(items[i].pos, items[i].opID, items[j].pos, items[j].opID)
		})

		want := []item{
			{pos: "a", opID: "1111"},
			{pos: "a", opID: "4444"},
			{pos: "aV", opID: "1111"},
			{pos: "aV", opID: "3333"},
			{pos: "b", opID: "2222"},
		}

		for i := range items {
			if items[i] != want[i] {
				t.Fatalf("item[%d] = %+v, want %+v", i, items[i], want[i])
			}
		}
	})
}
