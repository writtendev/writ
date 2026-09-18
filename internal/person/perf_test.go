package person_test

import (
	"strings"
	"testing"
	"time"

	"github.com/writtendev/writ/internal/person"
)

// longRunValue builds a value that is one starter followed by n marks in
// reverse canonical order, which is the worst case for the ordering step.
func longRunValue(n int) string {
	var b strings.Builder
	b.WriteString("user:a")
	for i := 0; i < n; i++ {
		// Cycle high classes down to low so every mark has to move.
		switch i % 4 {
		case 0:
			b.WriteRune(0x0301) // ccc 230
		case 1:
			b.WriteRune(0x0316) // ccc 220
		case 2:
			b.WriteRune(0x093C) // ccc 7
		default:
			b.WriteRune(0x0334) // ccc 1
		}
	}
	return b.String()
}

// TestNormalizeIsNotQuadratic guards an amplification vector rather than a
// micro-optimisation. Check is a producer-side guard that the fold
// deliberately does not call, so the length of a person identifier reaching
// normalization is bounded only by what an op body carries — attacker-chosen
// input, run by every reader of the repository, where the rule this replaced
// was linear.
//
// The assertion is a growth ratio rather than a wall-clock budget: quadratic
// scaling multiplies the time by ~16 when the input quadruples, linear-ish
// scaling by ~4. The threshold sits far enough above 4 to absorb a noisy
// machine and far enough below 16 to catch a regression.
func TestNormalizeIsNotQuadratic(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive")
	}
	measure := func(n int) time.Duration {
		v := longRunValue(n)
		// Warm up, then take the best of several runs so a scheduling stall
		// cannot manufacture a failure.
		person.NormalizePerson(v)
		best := time.Hour
		for i := 0; i < 5; i++ {
			start := time.Now()
			person.NormalizePerson(v)
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}

	small := measure(4000)
	large := measure(16000)
	ratio := float64(large) / float64(small)
	t.Logf("4000 marks: %v; 16000 marks: %v; ratio %.2fx (linear ~4x, quadratic ~16x)", small, large, ratio)
	if ratio > 9 {
		t.Errorf("normalization scales %.1fx for a 4x input, which is quadratic-shaped; "+
			"canonical ordering must stay near-linear", ratio)
	}
}

func BenchmarkNormalizePersonASCII(b *testing.B) {
	v := "email:alice@example.com"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		person.NormalizePerson(v)
	}
}

func BenchmarkNormalizePersonTypical(b *testing.B) {
	v := "email:José.Müller@example.com"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		person.NormalizePerson(v)
	}
}

func BenchmarkNormalizePersonAtBound(b *testing.B) {
	v := longRunValue(319)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		person.NormalizePerson(v)
	}
}

func BenchmarkNormalizePersonLongRun(b *testing.B) {
	v := longRunValue(16000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		person.NormalizePerson(v)
	}
}
