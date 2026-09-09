package fixtures

import "testing"

// TestLoadRejectsDuplicateRefNames covers the collision Generate can't
// safely resolve on its own: two refs (or a ref and a keep_as) sharing a
// name would make one silently overwrite the other in the real repo
// while both still ended up in the manifest as if they'd landed.
func TestLoadRejectsDuplicateRefNames(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "duplicate top-level ref",
			yaml: `
name: dup
refs:
  - name: refs/heads/main
    history:
      - commits:
          - {author: alice, timestamp: 2026-01-01T00:00:00Z, message: m, files: {f: "1"}}
  - name: refs/heads/main
    history:
      - commits:
          - {author: alice, timestamp: 2026-01-01T00:00:00Z, message: m, files: {f: "1"}}
`,
		},
		{
			name: "keep_as collides with another ref",
			yaml: `
name: dup
refs:
  - name: refs/heads/main
    history:
      - keep_as: refs/heads/other
        commits:
          - {author: alice, timestamp: 2026-01-01T00:00:00Z, message: m, files: {f: "1"}}
      - commits:
          - {author: alice, timestamp: 2026-01-01T00:00:01Z, message: m2, files: {f: "2"}}
  - name: refs/heads/other
    history:
      - commits:
          - {author: alice, timestamp: 2026-01-01T00:00:00Z, message: m, files: {f: "1"}}
`,
		},
		{
			name: "two keep_as values collide",
			yaml: `
name: dup
refs:
  - name: refs/heads/main
    history:
      - keep_as: refs/fixture-history/shared
        commits:
          - {author: alice, timestamp: 2026-01-01T00:00:00Z, message: m, files: {f: "1"}}
      - keep_as: refs/fixture-history/shared
        commits:
          - {author: alice, timestamp: 2026-01-01T00:00:01Z, message: m2, files: {f: "2"}}
      - commits:
          - {author: alice, timestamp: 2026-01-01T00:00:02Z, message: m3, files: {f: "3"}}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load([]byte(tc.yaml)); err == nil {
				t.Fatal("expected Load to reject a duplicate ref name, got nil error")
			}
		})
	}
}

func TestLoadRejectsInvalidCommitDescriptors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "duplicate commit id",
			yaml: `
name: dup-id
refs:
  - name: refs/heads/main
    history:
      - commits:
          - id: c1
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m1
            files: {f: "1"}
          - id: c1
            author: alice
            timestamp: 2026-01-01T00:01:00Z
            message: m2
            files: {f: "2"}
`,
		},
		{
			name: "unknown parent label",
			yaml: `
name: unknown-parent
refs:
  - name: refs/heads/main
    history:
      - commits:
          - parents: [nonexistent]
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
`,
		},
		{
			name: "forward parent reference",
			yaml: `
name: forward-parent
refs:
  - name: refs/heads/main
    history:
      - commits:
          - parents: [c2]
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m1
            files: {f: "1"}
          - id: c2
            author: alice
            timestamp: 2026-01-01T00:01:00Z
            message: m2
            files: {f: "2"}
`,
		},
		{
			name: "both op and files specified",
			yaml: `
name: op-and-files
refs:
  - name: refs/heads/main
    history:
      - commits:
          - author: alice
            timestamp: 2026-01-01T00:00:00Z
            files: {f: "1"}
            op:
              object_id: r1
              object_type: widget
              op_type: create
              op_version: 1
              body: {}
`,
		},
		{
			name: "both op and message specified",
			yaml: `
name: op-and-message
refs:
  - name: refs/heads/main
    history:
      - commits:
          - author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: custom message
            op:
              object_id: r1
              object_type: widget
              op_type: create
              op_version: 1
              body: {}
`,
		},
		{
			name: "invalid sign_as identity",
			yaml: `
name: invalid-sign-as
refs:
  - name: refs/heads/main
    history:
      - commits:
          - author: alice
            sign_as: unknown_identity
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
`,
		},
		{
			name: "invalid tamper enum",
			yaml: `
name: invalid-tamper
refs:
  - name: refs/heads/main
    history:
      - commits:
          - author: alice
            tamper: arbitrary-unsupported-tamper
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
`,
		},
		{
			name: "invalid reject reason",
			yaml: `
name: invalid-reject-reason
refs:
  - name: refs/heads/main
    history:
      - commits:
          - author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
            expect:
              reject: made-up-rejection-reason
`,
		},
		{
			name: "invalid disposition enum",
			yaml: `
name: invalid-disposition
refs:
  - name: refs/heads/main
    history:
      - commits:
          - author: alice
            timestamp: 2026-01-01T00:00:00Z
            disposition: unsupported-disposition
            op:
              object_id: r1
              object_type: widget
              op_type: create
              op_version: 1
              body: {}
`,
		},
		{
			name: "disposition without op",
			yaml: `
name: disposition-without-op
refs:
  - name: refs/heads/main
    history:
      - commits:
          - author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
            disposition: interpretable
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load([]byte(tc.yaml)); err == nil {
				t.Fatalf("expected Load to reject %s, got nil error", tc.name)
			}
		})
	}
}

func TestLoadAcceptsValidDisposition(t *testing.T) {
	yamlData := `
name: valid-disposition
refs:
  - name: refs/heads/main
    history:
      - commits:
          - id: c1
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            disposition: interpretable
            op:
              object_id: r1
              object_type: widget
              op_type: create
              op_version: 1
              body: {}
          - id: c2
            author: alice
            timestamp: 2026-01-01T00:01:00Z
            disposition: opaque
            op:
              object_id: r2
              object_type: custom
              op_type: action
              op_version: 1
              body: {}
`
	desc, err := Load([]byte(yamlData))
	if err != nil {
		t.Fatalf("expected Load to accept valid dispositions, got: %v", err)
	}
	if desc.Refs[0].History[0].Commits[0].Disposition != "interpretable" {
		t.Errorf("expected commit 0 disposition 'interpretable', got %q", desc.Refs[0].History[0].Commits[0].Disposition)
	}
	if desc.Refs[0].History[0].Commits[1].Disposition != "opaque" {
		t.Errorf("expected commit 1 disposition 'opaque', got %q", desc.Refs[0].History[0].Commits[1].Disposition)
	}
}

func TestLoadRejectsInvalidResolutions(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "resolution missing name",
			yaml: `
name: invalid-res
refs:
  - name: refs/heads/main
    history:
      - commits:
          - id: c1
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
resolutions:
  - anchor: {at: c1, path: f, side: new}
    target: c1
    expect: {outcome: resolved, match: exact-path-blob}
`,
		},
		{
			name: "unknown target commit label",
			yaml: `
name: invalid-res
refs:
  - name: refs/heads/main
    history:
      - commits:
          - id: c1
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
resolutions:
  - name: r1
    anchor: {at: c1, path: f, side: new}
    target: unknown-target
    expect: {outcome: resolved, match: exact-path-blob}
`,
		},
		{
			name: "unknown anchor commit label",
			yaml: `
name: invalid-res
refs:
  - name: refs/heads/main
    history:
      - commits:
          - id: c1
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
resolutions:
  - name: r1
    anchor: {at: unknown-at, path: f, side: new}
    target: c1
    expect: {outcome: resolved, match: exact-path-blob}
`,
		},
		{
			name: "invalid range format (end < start)",
			yaml: `
name: invalid-res
refs:
  - name: refs/heads/main
    history:
      - commits:
          - id: c1
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
resolutions:
  - name: r1
    anchor: {at: c1, path: f, side: new, range: [10, 5]}
    target: c1
    expect: {outcome: resolved, match: exact-path-blob}
`,
		},
		{
			name: "invalid match rung",
			yaml: `
name: invalid-res
refs:
  - name: refs/heads/main
    history:
      - commits:
          - id: c1
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
resolutions:
  - name: r1
    anchor: {at: c1, path: f, side: new}
    target: c1
    expect: {outcome: resolved, match: made-up-match}
`,
		},
		{
			name: "invalid orphan reason",
			yaml: `
name: invalid-res
refs:
  - name: refs/heads/main
    history:
      - commits:
          - id: c1
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
resolutions:
  - name: r1
    anchor: {at: c1, path: f, side: new}
    target: c1
    expect: {outcome: orphaned, reason: made-up-reason}
`,
		},
		{
			name: "resolved specifies orphan reason",
			yaml: `
name: invalid-res
refs:
  - name: refs/heads/main
    history:
      - commits:
          - id: c1
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
resolutions:
  - name: r1
    anchor: {at: c1, path: f, side: new}
    target: c1
    expect: {outcome: resolved, match: exact-path-blob, reason: path-absent}
`,
		},
		{
			name: "orphaned specifies match",
			yaml: `
name: invalid-res
refs:
  - name: refs/heads/main
    history:
      - commits:
          - id: c1
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: m
            files: {f: "1"}
resolutions:
  - name: r1
    anchor: {at: c1, path: f, side: new}
    target: c1
    expect: {outcome: orphaned, reason: path-absent, match: exact-path-blob}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load([]byte(tc.yaml)); err == nil {
				t.Fatalf("expected Load to reject %s, got nil error", tc.name)
			}
		})
	}
}

func TestLoadAcceptsValidResolutions(t *testing.T) {
	yamlData := `
name: valid-resolutions
refs:
  - name: refs/heads/main
    history:
      - commits:
          - id: base
            author: alice
            timestamp: 2026-01-01T00:00:00Z
            message: base
            files: {f: "1"}
          - id: target-commit
            author: bob
            timestamp: 2026-01-01T00:01:00Z
            message: rewrite
            files: {f: "2"}
resolutions:
  - name: res1
    anchor:
      at: base
      path: f
      side: new
      range: [1, 2]
    target: target-commit
    expect:
      outcome: resolved
      match: context-exact
  - name: res2
    anchor:
      old:
        at: base
        path: f
        range: [1, 1]
      new:
        at: base
        path: f
        range: [1, 2]
    target: target-commit
    expect:
      status: partially-resolved
      old:
        outcome: resolved
        match: exact-path-blob
      new:
        outcome: orphaned
        reason: path-absent
`
	desc, err := Load([]byte(yamlData))
	if err != nil {
		t.Fatalf("expected Load to accept valid resolutions, got: %v", err)
	}
	if len(desc.Resolutions) != 2 {
		t.Fatalf("expected 2 resolutions, got %d", len(desc.Resolutions))
	}
	if desc.Resolutions[0].Name != "res1" || desc.Resolutions[0].Expect.Match != "context-exact" {
		t.Errorf("unexpected resolution 0: %+v", desc.Resolutions[0])
	}
	if desc.Resolutions[1].Name != "res2" || desc.Resolutions[1].Expect.Status != "partially-resolved" {
		t.Errorf("unexpected resolution 1: %+v", desc.Resolutions[1])
	}
}


