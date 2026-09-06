package spec_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/writtendev/writ/spec"
)

func TestVocabularyEnumsExtractedFromSchemas(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{
			name: "ReviewStatuses",
			got:  spec.ReviewStatuses(),
			want: []string{"draft", "open", "closed", "merged"},
		},
		{
			name: "ApprovalVerdicts",
			got:  spec.ApprovalVerdicts(),
			want: []string{"approve", "request-changes", "none"},
		},
		{
			name: "CIStatusStates",
			got:  spec.CIStatusStates(),
			want: []string{"pending", "success", "failure", "error", "cancelled", "neutral", "skipped"},
		},
		{
			name: "LinkRelations",
			got:  spec.LinkRelations(),
			want: []string{"fixes", "relates", "none"},
		},
		{
			name: "ProjectStatuses",
			got:  spec.ProjectStatuses(),
			want: []string{"planned", "active", "paused", "completed", "canceled"},
		},
		{
			name: "WorkflowStateTypes",
			got:  spec.WorkflowStateTypes(),
			want: []string{"backlog", "unstarted", "started", "completed", "canceled"},
		},
		{
			name: "EstimateScales",
			got:  spec.EstimateScales(),
			want: []string{"none", "fibonacci", "exponential", "linear", "t-shirt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

// TestLinkRelationsAgreeAcrossTables pins that issue-ops and review-ops
// declare the same link.relation enum: WRIT-185 replaced the schema-scraping
// single source of truth with per-table rule declarations, so nothing stops
// two tables from drifting apart the way one shared schema $defs entry could
// not. Document's link.relation is deliberately not this enum (an
// unconstrained string, per spec/value-types.md) and is excluded.
func TestLinkRelationsAgreeAcrossTables(t *testing.T) {
	issue := spec.EnumValues("issue-ops", "link", "relation")
	review := spec.EnumValues("review-ops", "link", "relation")
	if !reflect.DeepEqual(issue, review) {
		t.Errorf("issue-ops link.relation = %v, review-ops link.relation = %v; want equal", issue, review)
	}
}

func TestFormatOptions(t *testing.T) {
	tests := []struct {
		input []string
		want  string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"single"}, "single"},
		{[]string{"open", "closed"}, "open or closed"},
		{[]string{"approve", "request-changes", "none"}, "approve, request-changes, or none"},
		{[]string{"draft", "open", "closed", "merged"}, "draft, open, closed, or merged"},
	}

	for _, tt := range tests {
		got := spec.FormatOptions(tt.input)
		if got != tt.want {
			t.Errorf("FormatOptions(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIssuePriorityVocabulary(t *testing.T) {
	names := spec.IssuePriorityNames()
	wantNames := []string{"none", "urgent", "high", "medium", "low"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("IssuePriorityNames() = %v, want %v", names, wantNames)
	}

	for i, name := range wantNames {
		// Parse by name
		got, err := spec.ParseIssuePriority(name)
		if err != nil {
			t.Errorf("ParseIssuePriority(%q): unexpected error %v", name, err)
		}
		if got != i {
			t.Errorf("ParseIssuePriority(%q) = %d, want %d", name, got, i)
		}

		// Parse case-insensitive
		gotUpper, err := spec.ParseIssuePriority(strings.ToUpper(name))
		if err != nil {
			t.Errorf("ParseIssuePriority(%q): unexpected error %v", strings.ToUpper(name), err)
		}
		if gotUpper != i {
			t.Errorf("ParseIssuePriority(%q) = %d, want %d", strings.ToUpper(name), gotUpper, i)
		}

		// Parse by digit
		digit := fmt.Sprintf("%d", i)
		gotDigit, err := spec.ParseIssuePriority(digit)
		if err != nil {
			t.Errorf("ParseIssuePriority(%q): unexpected error %v", digit, err)
		}
		if gotDigit != i {
			t.Errorf("ParseIssuePriority(%q) = %d, want %d", digit, gotDigit, i)
		}

		// Format
		formatted := spec.FormatIssuePriority(i)
		if formatted != name {
			t.Errorf("FormatIssuePriority(%d) = %q, want %q", i, formatted, name)
		}
	}

	// Invalid strings
	invalid := []string{"invalid", "5", "-1", "urgent!", ""}
	for _, inv := range invalid {
		if _, err := spec.ParseIssuePriority(inv); err == nil {
			t.Errorf("ParseIssuePriority(%q) expected error, got nil", inv)
		}
	}
}
