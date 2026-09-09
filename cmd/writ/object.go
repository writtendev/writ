// Command family `writ object` is the generic plumbing porcelain over any
// schema-declared collaborative object type: create, apply, show, and list,
// on top of the schema-shaped Store.Objects / Store.Query.Objects / Store.Types
// surface WRIT-192 shipped (ARCHITECTURE.md §Public API shape). `writ schema
// show` lives here too, alongside the vocabulary lookups `object` itself
// needs.
//
// Writ hard-codes no object type but `schema` itself, so it cannot offer a
// good per-type verb for one: `writ object create acme.ticket create -field
// title=...` is worse to type than a hand-written `writ ticket create
// -title ...` would be. That is expected -- nice per-type porcelain is a
// job for whatever layer owns the schema, built on --json. This file adds
// no schema-driven dynamic subcommand generation to compensate: help text,
// completion, flag types, and error messages would all become
// schema-dependent, for a CLI whose main consumer is agents reading --json
// anyway.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
)

func runObject(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		renderUsage(stderr, []string{"object"}, objectCmd)
		return 2
	}

	switch args[0] {
	case "-h", "-help", "--help":
		renderUsage(stdout, []string{"object"}, objectCmd)
		return 0
	case "create":
		return runObjectCreate(ctx, defaultDir, args[1:], stdout, stderr)
	case "apply":
		return runObjectApply(ctx, defaultDir, args[1:], stdout, stderr)
	case "show":
		return runObjectShow(ctx, defaultDir, args[1:], stdout, stderr)
	case "list":
		return runObjectList(ctx, defaultDir, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "writ object: unknown subcommand %q\n\n", args[0])
		renderUsage(stderr, []string{"object"}, objectCmd)
		return 2
	}
}

// declaredTypeNames lists every object type the installed vocabulary
// declares, sorted, for use in an "unknown type" error message.
func declaredTypeNames(types []writ.SchemaType) []string {
	names := make([]string, 0, len(types))
	for _, t := range types {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}

// typeIsQueryable reports whether name may be used as the <type> positional
// to `object list` or `schema show`: every type Store.Types declares, plus
// "schema" itself. Store.Types deliberately excludes "schema" (engine/
// schema.go: it is writ's one hard-coded object type, folded by FoldSchema
// rather than by ordinary schema-declared rules -- spec/schema-ops.md §1),
// but the projection's objects table carries real "schema" rows once
// `writ schema apply` has run. Refusing "schema" here as "not declared"
// would contradict object list's own rationale (never a silent empty
// result for a type that does have objects) for the one type that
// demonstrably does.
func typeIsQueryable(types []writ.SchemaType, name string) bool {
	if name == "schema" {
		return true
	}
	for _, t := range types {
		if t.Name == name {
			return true
		}
	}
	return false
}

// declaredOpNames lists the op types objectType declares, sorted and
// deduplicated across versions, for use in an "unknown op" error message.
func declaredOpNames(td *writ.SchemaType) []string {
	seen := make(map[string]bool)
	var names []string
	for _, o := range td.Ops {
		if !seen[o.OpType] {
			seen[o.OpType] = true
			names = append(names, o.OpType)
		}
	}
	sort.Strings(names)
	return names
}

// resolveOpVersion mirrors writ.Objects.Create/Apply's own op-version
// resolution (engine/objects.go's unexported resolveOpVersion, over the
// same Store.Types data): find objectType's declared version(s) of opType,
// refusing and naming the candidates when more than one exists. The CLI
// needs the concrete version before Create/Apply ever runs, to look up
// -field rules by the fully-specified (type, op_type, op_version) tuple
// below -- so this small duplicate of the engine's own decision, not a
// deeper one like a value-type catalogue, is unavoidable: passing the
// resolved version through to NewOp then means Create/Apply's identical
// check never has anything left to do.
//
// explicitVersion, when non-zero, is a caller-supplied -op-version: it is
// checked for membership in the declared version set instead of requiring
// exactly one, so an undeclared explicit version is refused here -- naming
// the versions that are declared -- rather than surfacing later as a
// misleading "field is not declared" error out of parseFieldFlags.
//
// "schema" gets its own refusal before the declared-types lookup below.
// Store.Types never returns a "schema" entry (see typeIsQueryable), so
// without this check it would fall into the generic "not declared by the
// installed vocabulary" branch -- which is wrong, and contradicts
// typeIsQueryable accepting "schema" for the read verbs (object list,
// schema show). schema objects are real and declared; they are just
// written through the dedicated writ schema plan/apply pipeline
// (FoldSchema), not through the generic object create/apply verbs this
// function backs, so the message needs to say that instead.
func resolveOpVersion(types []writ.SchemaType, objectType, opType string, explicitVersion int64) (int64, error) {
	if objectType == "schema" {
		return 0, fmt.Errorf("object type %q is writ's built-in vocabulary; write it with 'writ schema plan' and 'writ schema apply', not this command", objectType)
	}

	var td *writ.SchemaType
	for i := range types {
		if types[i].Name == objectType {
			td = &types[i]
			break
		}
	}
	if td == nil {
		return 0, fmt.Errorf("object type %q is not declared by the installed vocabulary (declares: %s)", objectType, strings.Join(declaredTypeNames(types), ", "))
	}

	versionSet := make(map[int64]bool)
	for _, o := range td.Ops {
		if o.OpType == opType {
			versionSet[o.OpVersion] = true
		}
	}
	if len(versionSet) == 0 {
		return 0, fmt.Errorf("object type %q declares no op %q (declares: %s)", objectType, opType, strings.Join(declaredOpNames(td), ", "))
	}

	versions := make([]int64, 0, len(versionSet))
	for v := range versionSet {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })

	if explicitVersion != 0 {
		if versionSet[explicitVersion] {
			return explicitVersion, nil
		}
		return 0, fmt.Errorf("object type %q declares op %q at version(s) %v, not %d", objectType, opType, versions, explicitVersion)
	}

	if len(versionSet) > 1 {
		return 0, fmt.Errorf("object type %q declares %d versions of op %q (%v): specify -op-version explicitly", objectType, len(versionSet), opType, versions)
	}
	return versions[0], nil
}

// lookupSchemaField finds the field rule declared for the fully-specified
// (object type, op type, op version, field name) tuple, or nil if the op
// declares no such field.
func lookupSchemaField(types []writ.SchemaType, objectType, opType string, opVersion int64, name string) *writ.SchemaField {
	for i := range types {
		if types[i].Name != objectType {
			continue
		}
		for j := range types[i].Fields {
			f := &types[i].Fields[j]
			if f.Name == name && f.OpType == opType && f.OpVersion == opVersion {
				return f
			}
		}
	}
	return nil
}

// lookupSchemaKeyColumn reports whether name is a keyed-lww key column of
// some field declared for the fully-specified (object type, op type, op
// version) tuple (spec/op-envelope.md §Producer validation rule 3): a
// keyed-lww field's key columns travel in the op body but are not
// themselves declared fields, so a caller that already tried
// lookupSchemaField and got nil checks here before refusing the name.
func lookupSchemaKeyColumn(types []writ.SchemaType, objectType, opType string, opVersion int64, name string) bool {
	for i := range types {
		if types[i].Name != objectType {
			continue
		}
		for j := range types[i].Fields {
			f := &types[i].Fields[j]
			if f.OpType != opType || f.OpVersion != opVersion || f.Strategy != "keyed-lww" {
				continue
			}
			if _, ok := f.KeyTypes[name]; ok {
				return true
			}
		}
	}
	return false
}

// declaredFieldNames lists the field and keyed-lww key column names the
// given (object type, op type, op version) actually declares, sorted and
// deduplicated, for use in an "undeclared field" error message -- a key
// column is exactly as declared as a field for -field/-field-json purposes
// (spec/op-envelope.md §Producer validation rule 3), so it belongs in the
// list a rejection points at.
func declaredFieldNames(types []writ.SchemaType, objectType, opType string, opVersion int64) []string {
	seen := make(map[string]bool)
	var names []string
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for i := range types {
		if types[i].Name != objectType {
			continue
		}
		for _, f := range types[i].Fields {
			if f.OpType != opType || f.OpVersion != opVersion {
				continue
			}
			add(f.Name)
			if f.Strategy == "keyed-lww" {
				for col := range f.KeyTypes {
					add(col)
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// convertFieldValue converts one raw --field string into the Go value
// op.Fields expects, by the field's declared value_type. This is
// type-directed parsing only, never re-validation: string, text, enum,
// person-ref, object-ref, git-oid, position, timestamp, and no declared
// value type all pass the raw string through unchanged, leaving enum
// membership, max_length, and pattern checks to the producer validator that
// already runs inside Objects.Create/Apply -- a CLI copy of
// engine/internal/value would be exactly the second, drifting copy
// WRIT-192's plan refused for Objects.Create itself.
func convertFieldValue(valueType, raw string) (any, error) {
	switch valueType {
	case "int":
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid int value %q", raw)
		}
		return n, nil
	case "number":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number value %q", raw)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("invalid number value %q", raw)
		}
		return f, nil
	case "bool":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid bool value %q", raw)
		}
		return b, nil
	case "anchor":
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("invalid anchor value %q: must be a JSON object", raw)
		}
		return m, nil
	default:
		// string, text, enum, person-ref, object-ref, git-oid, position,
		// timestamp, or no declared value type at all.
		return raw, nil
	}
}

// fieldEntry is one raw -field or -field-json value, in the order given,
// tagged with which flag it came from so parseFieldFlags can convert it
// correctly and refuse mixing the two flags for the same key.
type fieldEntry struct {
	raw     string
	viaJSON bool
}

// parseFieldFlags groups repeated -field/-field-json k=v flags by key,
// looks each key up as a field rule of (objectType, opType, opVersion), and
// returns the resulting op body. A key given more than once becomes a JSON
// array of the converted elements, in the order given; a key given once
// stays a scalar.
//
// -field converts its value by the field's declared value_type
// (convertFieldValue). -field-json instead decodes the value as JSON
// directly, with no type-directed conversion: it is the escape hatch for a
// field convertFieldValue cannot express -- an object-shaped field (only
// "anchor" gets object parsing from convertFieldValue; the closed value-type
// catalogue, spec/value-types.md, has no other object-shaped entry) or a
// field with no declared value type at all, such as an
// {object_type, object_id} record naming another object
// (spec/value-types.md's "untyped" exception: a two-field record folded
// whole under create-once, so there is no value_type to key type-directed
// parsing off of). See WRIT-209.
//
// A key given via -field that names a keyed-lww key column (lookupSchemaKeyColumn
// true) is a string pass-through, never routed through convertFieldValue's
// key_types entry, whether or not the same name is ALSO a declared field
// (lookupSchemaField non-nil): a key column's value MUST be a JSON string
// on the wire regardless of its declared value type (spec/op-envelope.md
// §Producer validation rule 3), so converting e.g. an "int"-typed key
// column to a JSON number here would hand the producer an op it can only
// reject -- and where the name is also a declared field, the field's own
// value_type is what the producer checks the string's *content* against
// (canonicalKeyColumnContent), not a reason to convert it to that type's
// ordinary JSON shape here. Checking lookupSchemaKeyColumn unconditionally,
// not only when lookupSchemaField returns nil, is what makes a dual-role
// field (spec/testdata/producer/cases/keyed-lww-key-column-also-a-field-int-decoded.json's
// shape) writable via -field at all: gating it on lookupSchemaField
// returning nil left convertFieldValue converting the dual-role case to
// its ordinary JSON shape instead, which the producer's key-column floor
// always refused. -field-json is unaffected either way -- it already
// decodes whatever JSON the caller wrote, key column or not.
func parseFieldFlags(fieldRaw, fieldJSONRaw []string, objectType, opType string, opVersion int64, types []writ.SchemaType) (map[string]any, error) {
	var order []string
	grouped := make(map[string][]fieldEntry)
	collect := func(raw []string, viaJSON bool) error {
		flagName := "-field"
		if viaJSON {
			flagName = "-field-json"
		}
		for _, kv := range raw {
			idx := strings.IndexByte(kv, '=')
			if idx < 0 {
				return fmt.Errorf("invalid %s %q: expected key=value", flagName, kv)
			}
			key, val := kv[:idx], kv[idx+1:]
			if _, ok := grouped[key]; !ok {
				order = append(order, key)
			}
			grouped[key] = append(grouped[key], fieldEntry{raw: val, viaJSON: viaJSON})
		}
		return nil
	}
	if err := collect(fieldRaw, false); err != nil {
		return nil, err
	}
	if err := collect(fieldJSONRaw, true); err != nil {
		return nil, err
	}

	fields := make(map[string]any, len(order))
	for _, key := range order {
		rule := lookupSchemaField(types, objectType, opType, opVersion, key)
		// lookupSchemaKeyColumn is checked unconditionally, not only when
		// rule == nil: a name can be both a declared field and another
		// rule's keyed-lww key column (spec/op-envelope.md §Producer
		// validation rule 3), and that dual role is exactly what
		// determines how -field converts it below, whether or not it is
		// also a declared field.
		isKeyColumn := lookupSchemaKeyColumn(types, objectType, opType, opVersion, key)
		if rule == nil && !isKeyColumn {
			declared := declaredFieldNames(types, objectType, opType, opVersion)
			if len(declared) == 0 {
				return nil, fmt.Errorf("field %q is not declared for %s %s (it declares no fields)", key, objectType, opType)
			}
			return nil, fmt.Errorf("field %q is not declared for %s %s (declares: %s)", key, objectType, opType, strings.Join(declared, ", "))
		}

		entries := grouped[key]
		for _, e := range entries[1:] {
			if e.viaJSON != entries[0].viaJSON {
				return nil, fmt.Errorf("field %q given via both -field and -field-json: use one or the other", key)
			}
		}

		converted := make([]any, len(entries))
		for i, e := range entries {
			var cv any
			var err error
			switch {
			case e.viaJSON:
				if uerr := json.Unmarshal([]byte(e.raw), &cv); uerr != nil {
					err = fmt.Errorf("invalid JSON value %q: %v", e.raw, uerr)
				}
			case isKeyColumn:
				// String pass-through: see the isKeyColumn note on
				// parseFieldFlags's doc comment above.
				cv = e.raw
			default:
				cv, err = convertFieldValue(rule.ValueType, e.raw)
			}
			if err != nil {
				flagName := "-field"
				if e.viaJSON {
					flagName = "-field-json"
				}
				return nil, fmt.Errorf("%s %s=%s: %v", flagName, key, e.raw, err)
			}
			converted[i] = cv
		}
		if len(converted) == 1 {
			fields[key] = converted[0]
		} else {
			fields[key] = converted
		}
	}
	return fields, nil
}

// renderObjectMutationErr is renderErr for the two commands that write op
// bodies through -field/-field-json (object create, object apply): on top
// of renderErr's generic handling, it points a caller at -field-json when
// the rejection names a keyed-lww key column, which is the one class of
// schema violation -field structurally cannot always self-diagnose --
// -field's string pass-through and type-directed conversion (parseFieldFlags)
// hand a key column's value to the producer unvalidated, deferring to
// exactly the check that then refuses it, so the rejection is the first
// place the caller learns the value was wrong. The engine's own message
// already names the working spelling where the producer can compute one
// (canonicalKeyColumnContent's "(want %q)", validateCanonicalTimestamp's
// same pattern); what it cannot name is the CLI flag that sets a value as
// literal JSON instead of leaving -field to convert or pass it through.
func renderObjectMutationErr(w io.Writer, err error) int {
	code := renderErr(w, err)
	var rejErr *codec.RejectError
	if errors.As(err, &rejErr) && rejErr.Reason == codec.RejectSchemaViolation && strings.Contains(rejErr.Error(), "key column") {
		fmt.Fprintln(w, "writ: a keyed-lww key column's value must already be its exact wire encoding -- "+
			"-field-json <key>=<json> sets it as literal JSON instead of -field's pass-through/conversion")
	}
	return code
}

type objectCreateOpts struct {
	dir       string
	fields    stringSliceFlag
	fieldJSON stringSliceFlag
	opVer     int64
	jsonMode  bool
}

func newObjectCreateFlagSet(defaultDir string) (*flag.FlagSet, *objectCreateOpts) {
	fs := flag.NewFlagSet("object create", flag.ContinueOnError)
	opts := &objectCreateOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.Var(&opts.fields, "field", "Field `<k>=<v>` to set on the creating op (repeatable; repeat the same key for a set)")
	fs.Var(&opts.fieldJSON, "field-json", "Field `<k>=<v>` to set from raw JSON, skipping type-directed conversion (repeatable; the escape hatch for an object-shaped or untyped field, such as an {object_type, object_id} record naming another object)")
	fs.Int64Var(&opts.opVer, "op-version", 0, "Explicit op `version` (default: resolved from the installed vocabulary)")
	fs.BoolVar(&opts.jsonMode, "json", false, "Output result as JSON")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"object", "create"}, objectCreateCmd)
	}
	return fs, opts
}

func runObjectCreate(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newObjectCreateFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) < 2 {
		fmt.Fprintln(stderr, "writ object create: <type> and <op-type> are required")
		fs.Usage()
		return 2
	}
	if len(posArgs) > 2 {
		fmt.Fprintf(stderr, "writ object create: unexpected arguments: %s\n", strings.Join(posArgs[2:], " "))
		fs.Usage()
		return 2
	}
	objectType, opType := posArgs[0], posArgs[1]

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	types, err := store.Types(ctx)
	if err != nil {
		return renderErr(stderr, err)
	}

	version, err := resolveOpVersion(types, objectType, opType, opts.opVer)
	if err != nil {
		fmt.Fprintf(stderr, "writ object create: %v\n", err)
		return 1
	}

	fields, err := parseFieldFlags(opts.fields, opts.fieldJSON, objectType, opType, version, types)
	if err != nil {
		fmt.Fprintf(stderr, "writ object create: %v\n", err)
		return 1
	}

	id, err := store.Objects.Create(ctx, objectType, writ.NewOp{Type: opType, Version: version, Fields: fields})
	if err != nil {
		return renderObjectMutationErr(stderr, err)
	}

	if opts.jsonMode {
		if err := emitJSON(stdout, wire.KindObjectCreate, wire.ObjectCreated{ObjectID: id, ObjectType: objectType}); err != nil {
			fmt.Fprintf(stderr, "writ object create: marshal json: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintln(stdout, id)
	return 0
}

type objectApplyOpts struct {
	dir       string
	fields    stringSliceFlag
	fieldJSON stringSliceFlag
	opVer     int64
	jsonMode  bool
}

func newObjectApplyFlagSet(defaultDir string) (*flag.FlagSet, *objectApplyOpts) {
	fs := flag.NewFlagSet("object apply", flag.ContinueOnError)
	opts := &objectApplyOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.Var(&opts.fields, "field", "Field `<k>=<v>` to set on the op (repeatable; repeat the same key for a set)")
	fs.Var(&opts.fieldJSON, "field-json", "Field `<k>=<v>` to set from raw JSON, skipping type-directed conversion (repeatable; the escape hatch for an object-shaped or untyped field, such as an {object_type, object_id} record naming another object)")
	fs.Int64Var(&opts.opVer, "op-version", 0, "Explicit op `version` (default: resolved from the installed vocabulary)")
	fs.BoolVar(&opts.jsonMode, "json", false, "Output result as JSON")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"object", "apply"}, objectApplyCmd)
	}
	return fs, opts
}

func runObjectApply(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newObjectApplyFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) < 2 {
		fmt.Fprintln(stderr, "writ object apply: <object-id> and <op-type> are required")
		fs.Usage()
		return 2
	}
	if len(posArgs) > 2 {
		fmt.Fprintf(stderr, "writ object apply: unexpected arguments: %s\n", strings.Join(posArgs[2:], " "))
		fs.Usage()
		return 2
	}
	opType := posArgs[1]

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	objectID, err := resolveObjectID(ctx, store, posArgs[0])
	if err != nil {
		return renderErr(stderr, err)
	}

	existing, err := store.Query.Object(objectID)
	if err != nil {
		return renderErr(stderr, err)
	}

	types, err := store.Types(ctx)
	if err != nil {
		return renderErr(stderr, err)
	}

	version, err := resolveOpVersion(types, existing.ObjectType, opType, opts.opVer)
	if err != nil {
		fmt.Fprintf(stderr, "writ object apply: %v\n", err)
		return 1
	}

	fields, err := parseFieldFlags(opts.fields, opts.fieldJSON, existing.ObjectType, opType, version, types)
	if err != nil {
		fmt.Fprintf(stderr, "writ object apply: %v\n", err)
		return 1
	}

	if err := store.Objects.Apply(ctx, objectID, writ.NewOp{Type: opType, Version: version, Fields: fields}); err != nil {
		return renderObjectMutationErr(stderr, err)
	}

	if opts.jsonMode {
		if err := emitJSON(stdout, wire.KindObjectApply, wire.ObjectApplied{ObjectID: objectID, OpType: opType}); err != nil {
			fmt.Fprintf(stderr, "writ object apply: marshal json: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "%s: applied %s\n", objectID, opType)
	return 0
}

type objectShowOpts struct {
	dir      string
	jsonMode bool
}

func newObjectShowFlagSet(defaultDir string) (*flag.FlagSet, *objectShowOpts) {
	fs := flag.NewFlagSet("object show", flag.ContinueOnError)
	opts := &objectShowOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.BoolVar(&opts.jsonMode, "json", false, "Output result as JSON")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"object", "show"}, objectShowCmd)
	}
	return fs, opts
}

func runObjectShow(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newObjectShowFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) == 0 || posArgs[0] == "" {
		fmt.Fprintln(stderr, "writ object show: object ID is required")
		fs.Usage()
		return 2
	}
	if len(posArgs) > 1 {
		fmt.Fprintf(stderr, "writ object show: unexpected arguments: %s\n", strings.Join(posArgs[1:], " "))
		fs.Usage()
		return 2
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	objectID, err := resolveObjectID(ctx, store, posArgs[0])
	if err != nil {
		return renderErr(stderr, err)
	}

	obj, err := store.Objects.Get(ctx, objectID)
	if err != nil {
		return renderErr(stderr, err)
	}

	if opts.jsonMode {
		if err := emitJSON(stdout, wire.KindObjectShow, wire.FromObject(obj)); err != nil {
			fmt.Fprintf(stderr, "writ object show: marshal json: %v\n", err)
			return 1
		}
		return 0
	}

	keys := make([]string, 0, len(obj.Fields))
	for k := range obj.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "object_id\t%s\n", obj.ObjectID)
	fmt.Fprintf(tw, "object_type\t%s\n", obj.ObjectType)
	for _, k := range keys {
		fmt.Fprintf(tw, "%s\t%s\n", k, fieldDisplay(obj.Fields[k]))
	}
	_ = tw.Flush()

	if len(obj.UnknownOps) > 0 {
		fmt.Fprintln(stdout, "Unknown ops:")
		for _, u := range obj.UnknownOps {
			fmt.Fprintf(stdout, "  %s %s v%d (%s)\n", u.ObjectType, u.OpType, u.OpVersion, u.Commit)
		}
	}

	return 0
}

// fieldDisplay renders one Object.Fields value for the human tabwriter
// view: a bare string prints unquoted, everything else (numbers, bools,
// maps, slices -- an object type nothing declares a Go shape for) prints
// as compact JSON.
func fieldDisplay(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

type objectListOpts struct {
	dir            string
	authors        stringSliceFlag
	text           string
	includeDeleted bool
	limit          int
	offset         int
	sortOrder      string
	jsonMode       bool
}

func newObjectListFlagSet(defaultDir string) (*flag.FlagSet, *objectListOpts) {
	fs := flag.NewFlagSet("object list", flag.ContinueOnError)
	opts := &objectListOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.Var(&opts.authors, "author", "Filter by author `<a>` name or email (repeatable)")
	fs.StringVar(&opts.text, "text", "", "Filter by text `<q>` match")
	fs.BoolVar(&opts.includeDeleted, "include-deleted", false, "Include deleted objects")
	fs.IntVar(&opts.limit, "limit", 0, "Maximum number `N` of objects to return")
	fs.IntVar(&opts.offset, "offset", 0, "Skip the first `N` matching objects")
	fs.StringVar(&opts.sortOrder, "sort", "", "Sort order `<order>` (created_at_asc, created_at_desc, updated_at_asc, updated_at_desc)")
	fs.BoolVar(&opts.jsonMode, "json", false, "Output result as JSON")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"object", "list"}, objectListCmd)
	}
	return fs, opts
}

func runObjectList(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newObjectListFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) > 1 {
		fmt.Fprintf(stderr, "writ object list: unexpected arguments: %s\n", strings.Join(posArgs[1:], " "))
		fs.Usage()
		return 2
	}

	if opts.limit < 0 {
		fmt.Fprintf(stderr, "writ object list: -limit must be non-negative, got %d\n", opts.limit)
		fs.Usage()
		return 2
	}
	if opts.offset < 0 {
		fmt.Fprintf(stderr, "writ object list: -offset must be non-negative, got %d\n", opts.offset)
		fs.Usage()
		return 2
	}

	var orderBy writ.OrderBy
	if opts.sortOrder != "" {
		orderBy, err = parseOrderBy(opts.sortOrder)
		if err != nil {
			fmt.Fprintf(stderr, "writ object list: %v\n", err)
			fs.Usage()
			return 2
		}
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	var typeFilter []string
	if len(posArgs) == 1 && posArgs[0] != "" {
		types, err := store.Types(ctx)
		if err != nil {
			return renderErr(stderr, err)
		}
		if !typeIsQueryable(types, posArgs[0]) {
			fmt.Fprintf(stderr, "writ object list: object type %q is not declared by the installed vocabulary (declares: %s)\n", posArgs[0], strings.Join(declaredTypeNames(types), ", "))
			return 1
		}
		typeFilter = []string{posArgs[0]}
	}

	results, err := store.Query.Objects(writ.ObjectFilter{
		Type:           typeFilter,
		Author:         opts.authors,
		Text:           opts.text,
		IncludeDeleted: opts.includeDeleted,
		OrderBy:        orderBy,
		Limit:          opts.limit,
		Offset:         opts.offset,
	})
	if err != nil {
		return renderErr(stderr, err)
	}

	if opts.jsonMode {
		if err := emitJSON(stdout, wire.KindObjectList, wire.FromObjectResultSummaries(results)); err != nil {
			fmt.Fprintf(stderr, "writ object list: marshal json: %v\n", err)
			return 1
		}
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	for _, r := range results {
		shortID := r.ObjectID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		author := authorDisplay(r.Author.Name, r.Author.Email)
		updatedAt := r.UpdatedAt.Format("2006-01-02 15:04:05")
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", shortID, r.ObjectType, author, updatedAt)
	}
	_ = tw.Flush()
	return 0
}

// authorDisplay renders an op's author as "name <email>", falling back to
// whichever of the two is present, or "-" when both are blank. Op authors
// come off op commits, including foreign ones, and nothing normalizes them
// on the read path, so a blank name or email is a real possibility here,
// not a defensive nicety.
func authorDisplay(name, email string) string {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	switch {
	case name != "" && email != "":
		return fmt.Sprintf("%s <%s>", name, email)
	case name != "":
		return name
	case email != "":
		return email
	default:
		return "-"
	}
}

type schemaShowOpts struct {
	dir      string
	jsonMode bool
}

func newSchemaShowFlagSet(defaultDir string) (*flag.FlagSet, *schemaShowOpts) {
	fs := flag.NewFlagSet("schema show", flag.ContinueOnError)
	opts := &schemaShowOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.BoolVar(&opts.jsonMode, "json", false, "Output result as JSON")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"schema", "show"}, schemaShowCmd)
	}
	return fs, opts
}

// runSchemaShow reports the vocabulary actually installed and folding right
// now (Store.Types): exactly what the schema objects in the log declare, and
// nothing at all on a repository that declares nothing.
// This is deliberately not what `writ schema plan`/`apply` answer
// (Store.Schema, the working-tree writ.schema file's own view) -- see
// schemaShowCmd's Long text.
func runSchemaShow(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newSchemaShowFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) > 1 {
		fmt.Fprintf(stderr, "writ schema show: unexpected arguments: %s\n", strings.Join(posArgs[1:], " "))
		fs.Usage()
		return 2
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	types, err := store.Types(ctx)
	if err != nil {
		return renderErr(stderr, err)
	}

	if len(posArgs) == 0 {
		if opts.jsonMode {
			if err := emitJSON(stdout, wire.KindSchemaShow, wire.FromSchemaTypeInfos(types)); err != nil {
				fmt.Fprintf(stderr, "writ schema show: marshal json: %v\n", err)
				return 1
			}
			return 0
		}
		// Deliberately the porcelain form: one bare type name per line,
		// nothing else -- so shell completion can be a plain
		// $(writ schema show) call with nothing to parse.
		for _, t := range types {
			fmt.Fprintln(stdout, t.Name)
		}
		return 0
	}

	name := posArgs[0]
	var found *writ.SchemaType
	for i := range types {
		if types[i].Name == name {
			found = &types[i]
			break
		}
	}
	if found == nil && name == "schema" {
		// "schema" is queryable (typeIsQueryable) but never a Store.Types
		// entry: its own vocabulary is hard-coded (spec/schema-ops.md),
		// not resolved into a SchemaType's Fields/Ops the way every
		// other type's is, so there is nothing to report beyond the bare
		// name -- printing "not declared" here would be as wrong as it is
		// for `object list schema`.
		found = &writ.SchemaType{Name: "schema"}
	}
	if found == nil {
		return renderErr(stderr, fmt.Errorf("object type %q is not declared by the installed vocabulary", name))
	}

	if opts.jsonMode {
		if err := emitJSON(stdout, wire.KindSchemaShow, wire.FromSchemaTypeInfo(*found)); err != nil {
			fmt.Fprintf(stderr, "writ schema show: marshal json: %v\n", err)
			return 1
		}
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "type\t%s\n", found.Name)
	if found.Description != "" {
		fmt.Fprintf(tw, "description\t%s\n", found.Description)
	}
	if found.Deprecated {
		fmt.Fprintf(tw, "deprecated\t%v\n", found.Deprecated)
	}
	_ = tw.Flush()

	if len(found.Ops) > 0 {
		fmt.Fprintln(stdout, "Ops:")
		otw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		for _, o := range found.Ops {
			fmt.Fprintf(otw, "  %s\tv%d\t%s\n", o.OpType, o.OpVersion, o.Description)
		}
		_ = otw.Flush()
	}

	if len(found.Fields) > 0 {
		fmt.Fprintln(stdout, "Fields:")
		ftw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		for _, f := range found.Fields {
			target := ""
			if f.Target != "" {
				target = "-> " + f.Target
			}
			fmt.Fprintf(ftw, "  %s\t%s v%d\t%s\t%s\t%s\n", f.Name, f.OpType, f.OpVersion, f.ValueType, f.Strategy, target)
		}
		_ = ftw.Flush()
	}

	return 0
}
