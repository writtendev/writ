package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/schemasrc"
	"github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/internal/textdiff"
	"github.com/writtendev/writ/internal/textsafe"
)

// schemaSourceFileName is the one working-tree file this command family
// reads and writes: spec/schema-source.md names it exactly this,
// unqualified, in every repository that has one.
const schemaSourceFileName = "writ.schema"

func runSchema(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		renderUsage(stderr, []string{"schema"}, schemaCmd)
		return 2
	}

	switch args[0] {
	case "-h", "-help", "--help":
		renderUsage(stdout, []string{"schema"}, schemaCmd)
		return 0
	case "plan":
		return runSchemaPlan(ctx, defaultDir, args[1:], stdout, stderr)
	case "apply":
		return runSchemaApply(ctx, defaultDir, args[1:], stdout, stderr)
	case "show":
		return runSchemaShow(ctx, defaultDir, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "writ schema: unknown command %q\n\n", args[0])
		renderUsage(stderr, []string{"schema"}, schemaCmd)
		return 2
	}
}

type schemaPlanOpts struct {
	dir      string
	jsonMode bool
}

func newSchemaPlanFlagSet(defaultDir string) (*flag.FlagSet, *schemaPlanOpts) {
	fs := flag.NewFlagSet("schema plan", flag.ContinueOnError)
	opts := &schemaPlanOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in <dir>")
	fs.BoolVar(&opts.jsonMode, "json", false, "Output machine-readable JSON")
	return fs, opts
}

type schemaApplyOpts struct {
	dir      string
	jsonMode bool
}

func newSchemaApplyFlagSet(defaultDir string) (*flag.FlagSet, *schemaApplyOpts) {
	fs := flag.NewFlagSet("schema apply", flag.ContinueOnError)
	opts := &schemaApplyOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in <dir>")
	fs.BoolVar(&opts.jsonMode, "json", false, "Output machine-readable JSON")
	return fs, opts
}

func runSchemaPlan(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newSchemaPlanFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(posArgs) > 0 {
		fmt.Fprintf(stderr, "writ schema plan: unexpected arguments: %s\n", strings.Join(posArgs, " "))
		fs.Usage()
		return 2
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	// plan appends no ops: writ.Open auto-refreshes the SQLite projection
	// cache by default, which is a write, so plan disables it explicitly.
	// Store.Schema reads the DAG directly and needs no projection at all.
	store, err := openStore(targetDir, writ.WithoutAutoRefresh())
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	planRes, err := buildSchemaPlan(ctx, store, targetDir)
	if err != nil {
		return renderSchemaError(stderr, err)
	}

	if opts.jsonMode {
		if err := emitJSON(stdout, wire.KindSchemaPlan, planRes.toWirePlan()); err != nil {
			fmt.Fprintf(stderr, "writ schema plan: marshal json: %v\n", err)
			return 1
		}
		return 0
	}

	renderSchemaPlanPorcelain(stdout, planRes)
	return 0
}

func runSchemaApply(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newSchemaApplyFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(posArgs) > 0 {
		fmt.Fprintf(stderr, "writ schema apply: unexpected arguments: %s\n", strings.Join(posArgs, " "))
		fs.Usage()
		return 2
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir, writ.WithoutAutoRefresh())
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	// apply runs the whole of plan every time; it never trusts a previous
	// run, because there is no plan artifact and no recorded target id —
	// the log is the record.
	planRes, err := buildSchemaPlan(ctx, store, targetDir)
	if err != nil {
		return renderSchemaError(stderr, err)
	}

	if err := store.ApplySchema(ctx, planRes.ops); err != nil {
		return renderErr(stderr, err)
	}

	if opts.jsonMode {
		applyRes := schemaApplyResult{
			objectID:   planRes.objectID,
			namespace:  planRes.namespace,
			namespaces: planRes.namespaces,
			created:    planRes.created,
			ops:        planRes.ops,
		}
		if err := emitJSON(stdout, wire.KindSchemaApply, applyRes.toWireApply()); err != nil {
			fmt.Fprintf(stderr, "writ schema apply: marshal json: %v\n", err)
			return 1
		}
		return 0
	}

	if len(planRes.ops) == 0 {
		fmt.Fprintln(stdout, "writ.schema matches the schema in the log; nothing to apply.")
		return 0
	}
	if planRes.created {
		fmt.Fprintf(stdout, "Created schema object %s (namespace %q).\n", planRes.objectID, planRes.namespace)
		// Printed on every mint, including the first ever apply in a
		// repository (WRIT-223): what was missing was never the fact that
		// something was created -- schema.go already reported that since
		// WRIT-191 -- but the context that makes an accidental
		// `namespace acme` -> `namespace acme2` edit legible. A count
		// threshold here would be a magic condition for no reason; "1
		// namespace: acme" on the very first apply reads correctly.
		word := "namespace"
		if len(planRes.namespaces) != 1 {
			word = "namespaces"
		}
		fmt.Fprintf(stdout, "This repository now declares %d %s: %s.\n", len(planRes.namespaces), word, strings.Join(planRes.namespaces, ", "))
	} else {
		fmt.Fprintf(stdout, "Updated schema object %s (namespace %q).\n", planRes.objectID, planRes.namespace)
	}
	fmt.Fprintf(stdout, "Appended %d op(s).\n", len(planRes.ops))
	return 0
}

// schemaApplyResult is the small slice of schemaPlanResult apply's JSON
// output needs; kept distinct from schemaPlanResult so a rendering it
// doesn't compute (current_source, planned_source, up_to_date) can't leak
// into apply's own payload by accident.
type schemaApplyResult struct {
	objectID   string
	namespace  string
	namespaces []string
	created    bool
	ops        []codec.Envelope
}

func (r schemaApplyResult) toWireApply() wire.SchemaApply {
	namespaces := r.namespaces
	if namespaces == nil {
		namespaces = []string{}
	}
	return wire.SchemaApply{
		ObjectID:    r.objectID,
		Namespace:   r.namespace,
		Namespaces:  namespaces,
		Created:     r.created,
		OpsAppended: len(r.ops),
		Ops:         wire.FromSchemaEnvelopes(r.ops),
	}
}

// schemaError distinguishes an invalid writ.schema file or a refused plan
// (exit 1, per WRIT-191 correction 6) from a usage error (exit 2) or any
// other engine failure renderErr already knows how to report.
type schemaError struct {
	msgs []string
}

func (e *schemaError) Error() string {
	return strings.Join(e.msgs, "\n")
}

func renderSchemaError(w io.Writer, err error) int {
	var se *schemaError
	if errors.As(err, &se) {
		for _, m := range se.msgs {
			fmt.Fprintln(w, m)
		}
		return 1
	}
	var synErrs schemasrc.ErrorList
	if errors.As(err, &synErrs) {
		for _, e := range synErrs {
			fmt.Fprintln(w, e.Error())
		}
		return 1
	}
	var synErr *schemasrc.SyntaxError
	if errors.As(err, &synErr) {
		fmt.Fprintln(w, synErr.Error())
		return 1
	}
	return renderErr(w, err)
}

// schemaPlanResult is what buildSchemaPlan computes: the target object,
// whether applying would mint it fresh, the delta ops apply would append,
// both source renderings, and any repository-wide schema conflicts.
type schemaPlanResult struct {
	objectID      string
	namespace     string
	namespaces    []string
	created       bool
	upToDate      bool
	ops           []codec.Envelope
	currentSource []byte
	plannedSource []byte
	conflicts     []writ.SchemaConflict
}

func (r *schemaPlanResult) toWirePlan() wire.SchemaPlan {
	return wire.SchemaPlan{
		ObjectID:      r.objectID,
		Namespace:     r.namespace,
		Created:       r.created,
		UpToDate:      r.upToDate,
		Ops:           wire.FromSchemaEnvelopes(r.ops),
		CurrentSource: string(r.currentSource),
		PlannedSource: string(r.plannedSource),
		Conflicts:     wire.FromSchemaConflicts(r.conflicts),
	}
}

// buildSchemaPlan is the one computation both `plan` and `apply` run: parse
// the working-tree file, resolve which schema object it targets, refuse any
// edit that would remove a declaration, and compute the ops that would
// bring the log in line with the file. It never writes anything; `apply`
// calls it and then appends planRes.ops itself.
func buildSchemaPlan(ctx context.Context, store *writ.Store, dir string) (*schemaPlanResult, error) {
	gitInfo, err := writ.ResolveGitDir(dir)
	if err != nil {
		return nil, fmt.Errorf("writ: %w", err)
	}
	if gitInfo.WorkTree == "" {
		return nil, &schemaError{msgs: []string{"writ schema: no working tree (bare repository); writ.schema is a working-tree source file"}}
	}

	path := filepath.Join(gitInfo.WorkTree, schemaSourceFileName)
	src, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &schemaError{msgs: []string{
				fmt.Sprintf("writ schema: %s not found; run `writ init` to create a starter file", path),
			}}
		}
		return nil, fmt.Errorf("writ schema: read %s: %w", path, err)
	}

	f, err := schemasrc.Parse(schemaSourceFileName, src)
	if err != nil {
		return nil, err
	}

	schemas, err := store.Schema(ctx)
	if err != nil {
		return nil, fmt.Errorf("writ schema: %w", err)
	}
	_, conflicts := writ.RulesFromSchemas(schemas)

	objectID, err := resolveSchemaTarget(schemas, f)
	if err != nil {
		return nil, &schemaError{msgs: []string{err.Error()}}
	}

	current := schemaByObjectID(schemas, objectID)
	created := current.ObjectID == ""

	compiled, err := schemasrc.Compile(f, objectID)
	if err != nil {
		return nil, err
	}

	planned, err := writ.SchemaFromEnvelopes(compiled)
	if err != nil {
		return nil, fmt.Errorf("writ schema: %w", err)
	}

	delta, err := schemaDelta(current, compiled)
	if err != nil {
		return nil, fmt.Errorf("writ schema: %w", err)
	}

	// schemaRemovals runs first, ahead of conflictsIntroducedByApply: an
	// attribute narrowing is always also an instance of the divergence
	// conflictsIntroducedByApply exists to catch (see SchemaAfterApply's
	// doc comment), but schemaRemovals names the actual field and
	// attribute and states the remedy, which is more useful to a caller
	// than the field-rule-validation symptom conflictsIntroducedByApply
	// would otherwise report for the same edit.
	problems, err := schemaRemovals(current, planned, compiled)
	if err != nil {
		return nil, fmt.Errorf("writ schema: %w", err)
	}
	if len(problems) > 0 {
		msgs := make([]string, 0, len(problems)+1)
		msgs = append(msgs, "writ schema: refusing to plan (nothing is ever removed from the log):")
		for _, p := range problems {
			msgs = append(msgs, "  - "+p)
		}
		return nil, &schemaError{msgs: msgs}
	}

	// conflictsIntroducedByApply has to check the schema state a real
	// apply of delta would actually leave the log holding, not the state
	// folding the file alone produces (planned, above): those two
	// coincide for any change RulesFromSchemas would accept, but a
	// define-field op that narrows an attribute the log already carries
	// for that field is where they diverge — planned shows the file's own
	// narrowed declaration, while the log, after a real apply, would still
	// hold the old attribute alongside it. schemaRemovals, above, already
	// refuses every narrowing before an apply could ever reach the log
	// with it, but conflictsIntroducedByApply still has to see the honest
	// state to be correct in general, for every conflict class
	// RulesFromSchemas knows about, not only the one divergence class
	// schemaRemovals happens to also catch.
	honestPlanned, err := store.SchemaAfterApply(ctx, objectID, delta)
	if err != nil {
		return nil, fmt.Errorf("writ schema: %w", err)
	}

	// resolveSchemaTarget's guards compare this file's declared types
	// against the schema objects already in the log — every route to an
	// append is covered for a real, second schema object. What that
	// comparison cannot see is `schema` itself: the engine's one
	// hard-coded bootstrap type is never a schema object in `schemas`, so
	// no cross-object check ever runs for it, yet RulesFromSchemas refuses
	// to let any object bind it. Rather than special-case that one type
	// name, re-run RulesFromSchemas over the state this apply would
	// actually produce (schemas with the target's entry replaced, or
	// appended for a fresh mint, by honestPlanned) and refuse any conflict
	// that substitution introduces — present conflicts this repository
	// already has keep being reported, never refused, exactly as before;
	// only the delta this apply would be responsible for is new.
	if introduced := conflictsIntroducedByApply(schemas, conflicts, honestPlanned); len(introduced) > 0 {
		msgs := make([]string, 0, len(introduced)+1)
		msgs = append(msgs, "writ schema: refusing to apply (this would introduce a new schema conflict, permanently withholding rules for it):")
		for _, c := range introduced {
			msgs = append(msgs, "  - "+describeSchemaConflict(c))
		}
		return nil, &schemaError{msgs: msgs}
	}

	// A brand-new target has no rendering of its own yet — current is the
	// zero Schema{}, and Render requires a namespace matching
	// namespacePattern, which "" never does. Render only what actually
	// exists in the log; an empty current_source reads correctly as a diff
	// against nothing, which is exactly what creating an object is.
	//
	// currentSource and plannedSource are Render's raw output: no textsafe
	// pass runs here. docs/cli-json.md promises current_source and
	// planned_source are "writ.schema source text rendered from the log's
	// own folded state", and toWirePlan below hands these bytes to
	// emitJSON verbatim -- which already escapes every forbidden code
	// point in the whole marshalled document losslessly (a \uXXXX escape
	// sequence sitting inside an otherwise-valid JSON string decodes back
	// to the exact original rune; the same property object show --json
	// already relies on). Escaping a description here, before Render
	// quotes it, was round 4's finding 2 on PR #185: schemasrc.quoteString
	// backslash-escapes any backslash a prior escape pass introduced,
	// which both breaks losslessness for --json and collapses two
	// distinct descriptions onto the same rendered text, so a real change
	// could diff as empty. The human-readable diff has no JSON layer to
	// lean on for its own escaping, so renderSchemaPlanPorcelain below
	// escapes a local copy of these bytes -- after Render has already
	// quoted every description -- immediately before diffing them.
	var currentSource []byte
	if !created {
		currentSource, err = schemasrc.Render(current)
		if err != nil {
			return nil, fmt.Errorf("writ schema: render current schema: %w", err)
		}
	}
	plannedSource, err := schemasrc.Render(planned)
	if err != nil {
		return nil, fmt.Errorf("writ schema: render planned schema: %w", err)
	}

	return &schemaPlanResult{
		objectID:      objectID,
		namespace:     f.Namespace,
		namespaces:    schemaNamespaces(schemas, f.Namespace),
		created:       created,
		upToDate:      len(delta) == 0,
		ops:           delta,
		currentSource: currentSource,
		plannedSource: plannedSource,
		conflicts:     conflicts,
	}, nil
}

// schemaNamespaces returns the distinct, sorted, non-empty namespaces the
// repository's schema objects declare, unioned with extra -- the namespace
// this apply targets, so a fresh mint's own namespace is counted even
// before store.ApplySchema writes it. Loops over schemas, already in hand
// from store.Schema(ctx): no extra store call, no new engine API, no
// projection read. Always non-nil so a caller serializing it needs no nil
// check.
func schemaNamespaces(schemas []state.Schema, extra string) []string {
	set := make(map[string]bool, len(schemas)+1)
	for _, s := range schemas {
		if s.Namespace != "" {
			set[s.Namespace] = true
		}
	}
	if extra != "" {
		set[extra] = true
	}
	out := make([]string, 0, len(set))
	for ns := range set {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

// resolveSchemaTarget implements the one decision this ticket owns: which
// schema object `apply` writes to (WRIT-191 plan, "Which schema object
// apply writes to"). Computed fresh on every run — there is no plan
// artifact and no recorded id, and no --object-id flag: every case that
// flag would serve is a repository already in the state this function
// exists to describe.
//
// Namespace match count is the only signal available: zero means a fresh
// object (case 0), exactly one means reuse (case 1), and more than one is
// itself a pre-existing collision in the log this function refuses to add
// to (default). An earlier revision also refused case 0 and case 1 when
// f's own declared types collided with some *other* schema object's
// already-bound object_type — sensible while object_type was bare, when
// that really was one wire type bound twice, but WRIT-217 namespace-
// qualifies object_type precisely so two schema objects *can* bind the
// identical bare type name under different namespaces with zero collision
// (spec/schema-ops.md §2,
// TestSchemaCLI_DifferentNamespacesSameBareTypeBothInstall). Once that
// guarantee holds, "f's bare type overlaps some other object's bare type"
// can no longer tell a genuine collision apart from that exact, sanctioned
// case: editing `namespace acme` to `namespace acme2` in an already-
// applied writ.schema is observably identical, from this function's
// inputs, to a brand-new file that coincidentally reuses another
// namespace's type name — both are zero namespace matches, and the
// file's own type names are the only other data here. There is no way to
// refuse one without also refusing the other, so the guard is gone, not
// weakened: editing a namespace mints an independent schema object
// (TestSchemaCLI_NamespaceChangeMintsIndependentObject). The old object
// is untouched — namespace is create-once (§3.1) and this function never
// writes to an existing object's namespace field — and the new one is no
// more or less legitimate than an unrelated namespace declaring the same
// bare type for the first time.
func resolveSchemaTarget(schemas []state.Schema, f *schemasrc.File) (string, error) {
	var matches []state.Schema
	for _, s := range schemas {
		if s.Namespace == f.Namespace {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0].ObjectID, nil
	case 0:
		return deriveSchemaObjectID(f.Namespace), nil
	default:
		ids := make([]string, 0, len(matches))
		for _, s := range matches {
			ids = append(ids, s.ObjectID)
		}
		sort.Strings(ids)
		return "", fmt.Errorf(
			"writ schema: namespace %q is declared by more than one schema object (%s); resolve the collision in the log before running `writ schema apply`",
			f.Namespace, strings.Join(ids, ", "))
	}
}

// deriveSchemaObjectID returns the schema object id for namespace:
// "schema:" + namespace, per spec/identifiers.md's schema carve-out. A
// schema object's identity is its namespace, so two writers bootstrapping
// the same namespace offline derive the same id and converge on the same
// object instead of minting two that both bind the same object_type(s) —
// the collision RulesFromSchemas has no way to resolve. namespacePattern
// (engine/schemasrc/parse.go) constrains namespace to
// ^[a-z][a-z0-9-]*$, maxLength 64, so the result is always 8-71 characters
// of printable non-space ASCII: envelope-legal for every legal namespace,
// and never confusable with a minted id, since ^[0-9a-f]{32}$ admits no
// colon.
func deriveSchemaObjectID(namespace string) string {
	return "schema:" + namespace
}

// conflictsIntroducedByApply computes which of RulesFromSchemas' conflicts
// would exist only after this apply, not before: the delta between
// running it over the schemas already in the log (schemas, and the
// conflicts already computed from them, before) and running it again over
// that same set with the target object's entry replaced — or, for a
// brand-new object, appended — by planned, the state this apply would
// actually produce. Every conflict present in both is pre-existing and
// none of this apply's doing; every conflict only in the second run is one
// this apply would introduce.
//
// This is the general form: it catches every conflict class
// RulesFromSchemas knows about, not only the `object_type` collision
// between two real schema objects resolveSchemaTarget's own guards already
// refuse before buildSchemaPlan ever reaches here. The one it exists to
// catch that nothing else does is `type schema` — the engine's one
// hard-coded bootstrap type, never itself a schema object in `schemas`, so
// resolveSchemaTarget's cross-object comparison has nothing to check it
// against, but RulesFromSchemas refuses to let any object bind it and
// would report exactly that conflict once planned's own types are folded
// in.
func conflictsIntroducedByApply(schemas []state.Schema, before []writ.SchemaConflict, planned state.Schema) []writ.SchemaConflict {
	after := make([]state.Schema, 0, len(schemas)+1)
	replaced := false
	for _, s := range schemas {
		if s.ObjectID == planned.ObjectID {
			after = append(after, planned)
			replaced = true
			continue
		}
		after = append(after, s)
	}
	if !replaced {
		after = append(after, planned)
	}

	_, afterConflicts := writ.RulesFromSchemas(after)

	seen := make(map[string]bool, len(before))
	for _, c := range before {
		seen[conflictKey(c)] = true
	}

	var introduced []writ.SchemaConflict
	for _, c := range afterConflicts {
		if !seen[conflictKey(c)] {
			introduced = append(introduced, c)
		}
	}
	return introduced
}

// conflictKey renders a SchemaConflict as a comparable value so
// conflictsIntroducedByApply can tell "already there" from "new" by
// content rather than identity — RulesFromSchemas allocates a fresh
// []SchemaConflict on every call, so no two conflicts from different
// calls are ever the same slice element even when they describe the exact
// same collision. ObjectIDs is sorted before joining: RulesFromSchemas
// orders it (owner, then the colliding object) deterministically today,
// but this key does not depend on that holding forever.
func conflictKey(c writ.SchemaConflict) string {
	ids := append([]string(nil), c.ObjectIDs...)
	sort.Strings(ids)
	return strings.Join([]string{c.ObjectType, c.Namespace, strings.Join(ids, ","), c.Reason}, "\x00")
}

// describeSchemaConflict renders one SchemaConflict as a refusal line,
// naming whichever of object_type/namespace the conflict carries, its
// reason, and the schema object(s) involved.
func describeSchemaConflict(c writ.SchemaConflict) string {
	switch {
	case c.ObjectType != "":
		return fmt.Sprintf("object_type %q: %s (schema object(s): %s)", c.ObjectType, c.Reason, strings.Join(c.ObjectIDs, ", "))
	case c.Namespace != "":
		return fmt.Sprintf("namespace %q: %s (schema object(s): %s)", c.Namespace, c.Reason, strings.Join(c.ObjectIDs, ", "))
	default:
		return c.Reason
	}
}

// schemaByObjectID returns the folded state for objectID, or the zero
// Schema{} if the repository has no schema object with that id yet — the
// signal buildSchemaPlan uses to tell "creation" from "extending an
// existing object" (Schema{}.ObjectID == "").
func schemaByObjectID(schemas []state.Schema, objectID string) state.Schema {
	for _, s := range schemas {
		if s.ObjectID == objectID {
			return s
		}
	}
	return state.Schema{}
}

func findSchemaType(s state.Schema, name string) (state.SchemaType, bool) {
	for _, t := range s.Types {
		if t.Name == name {
			return t, true
		}
	}
	return state.SchemaType{}, false
}

func findSchemaOp(t state.SchemaType, opType string, opVersion int64) (state.SchemaOp, bool) {
	for _, o := range t.Ops {
		if o.OpType == opType && o.OpVersion == opVersion {
			return o, true
		}
	}
	return state.SchemaOp{}, false
}

func findSchemaField(t state.SchemaType, opType string, opVersion int64, field string) (state.SchemaField, bool) {
	for _, fl := range t.Fields {
		if fl.OpType == opType && fl.OpVersion == opVersion && fl.Name == field {
			return fl, true
		}
	}
	return state.SchemaField{}, false
}

// schemaFieldAttributeKeys lists the define-field body keys
// state.FoldSchema treats as independent keyed-lww registers
// (engine/state/schema.go): each is overwritten only when a later op's
// body actually carries that key, so a new define-field op whose body
// omits one does not clear the log's existing value — it leaves the log
// holding an attribute the file no longer declares, forever. There is no
// op that clears one of these, and there deliberately never will be
// (spec/schema-ops.md §8.1, ARCHITECTURE.md §Schema layer, WRIT-200):
// narrowing an attribute takes a new op_version instead, a distinct
// target only for the ones schemaFieldTargetSensitive names. So narrowing
// any of them is a removal in every sense schemaRemovals already refuses
// others for.
var schemaFieldAttributeKeys = []string{"value_type", "enum", "max_length", "lattice", "key", "key_types", "target"}

// schemaFieldTargetSensitive names the schemaFieldAttributeKeys entries a
// version bump cannot narrow under the field's existing target:
// `lattice` because newLatticeAccumulator (engine/internal/fold/
// strategy.go) builds its rank map once, from whichever matched rule
// Fold instantiates the target's single accumulator from, so two rules
// sharing a target and disagreeing on `lattice` are order-dependent
// (excluded from §8's "MAY freely change" bullet for exactly that
// reason, the same reason a `strategy` change MUST declare a distinct
// target — WRIT-206, spec/fieldrules.go's CheckTargetAgreement); `target`
// itself, whose narrowing is by definition a target change; and `key`
// and `key_types`, because ValidateFieldRule (spec/fieldrules.go)
// requires both exactly when strategy is keyed-lww and forbids them
// otherwise, so a redeclaration that stops carrying `key` (this
// function only runs on an attribute schemaRemovals found entirely
// absent — see there) has necessarily also stopped declaring
// `strategy: keyed-lww`. That entailed strategy change is the whole
// reason key/key_types are here; they are not order-dependent in
// themselves, since keyedLWWAccumulator.Apply reads Key and KeyTypes
// off the matched rule on every op rather than capturing them at
// construction, exactly as it reads ValueType. So §8's MAY bullet still
// lists key/key_types correctly: it covers a version bump that keeps
// both present and only changes their value (narrowing which columns
// compose the key while staying keyed-lww), which never reaches
// schemaRemovals's removed-attribute check at all, because the
// attribute is never absent from the body, only different. Every
// remaining entry is likewise unaffected by which rule the fold sees
// first at a shared target: enum and max_length are validation-only and
// the fold never reads them, and value_type, though read on every op to
// normalize a value, is read off the matched rule rather than captured
// at construction. So §8 already lets a version bump narrow any of the
// three under the same target (spec/schema-ops.md §8.1).
var schemaFieldTargetSensitive = map[string]bool{"lattice": true, "target": true, "key": true, "key_types": true}

// schemaAttributeNarrowingAdvice is the recipe schemaRemovals points a
// refused narrowing at, matching spec/schema-ops.md §8.1's split exactly.
func schemaAttributeNarrowingAdvice(attr string) string {
	if schemaFieldTargetSensitive[attr] {
		return "declare a new op_version with a distinct target instead"
	}
	return "declare a new op_version instead; the same target is fine"
}

// schemaFieldHasAttribute reports whether current's folded state carries a
// non-zero value for one of schemaFieldAttributeKeys.
func schemaFieldHasAttribute(f state.SchemaField, attr string) bool {
	switch attr {
	case "value_type":
		return f.ValueType != ""
	case "enum":
		return len(f.Enum) > 0
	case "max_length":
		return f.MaxLength != 0
	case "lattice":
		return len(f.Lattice) > 0
	case "key":
		return len(f.Key) > 0
	case "key_types":
		return len(f.KeyTypes) > 0
	case "target":
		return f.Target != ""
	default:
		return false
	}
}

// compiledFieldBody finds the one define-field envelope compiled emits for
// (typ, opType, opVersion, field) — schemasrc.Compile emits exactly one,
// carrying every attribute the file currently declares for it, whenever
// the file still declares the field at all — and returns its body decoded
// as raw key presence, so a caller can tell "declared with this key
// absent" from "declared with this key present but zero-valued", which
// schemaDefineFieldBody's typed decode (used for delta comparison
// elsewhere) cannot: a Go zero value and an absent JSON key are the same
// struct value once unmarshaled.
func compiledFieldBody(compiled []codec.Envelope, typ, opType string, opVersion int64, field string) (map[string]json.RawMessage, bool, error) {
	for _, env := range compiled {
		if env.OpType != "define-field" {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(env.Body, &raw); err != nil {
			return nil, false, fmt.Errorf("define-field body: %w", err)
		}
		if rawFieldString(raw, "type") != typ || rawFieldString(raw, "op_type") != opType || rawFieldString(raw, "field") != field {
			continue
		}
		verStr := rawFieldString(raw, "op_version")
		ver, err := strconv.ParseInt(verStr, 10, 64)
		if err != nil {
			return nil, false, fmt.Errorf("define-field op_version %q: %w", verStr, err)
		}
		if ver != opVersion {
			continue
		}
		return raw, true, nil
	}
	return nil, false, nil
}

// rawFieldString reads one string-valued key out of a raw decoded JSON
// object, or "" if it is absent or not a string.
func rawFieldString(raw map[string]json.RawMessage, key string) string {
	var s string
	if err := json.Unmarshal(raw[key], &s); err != nil {
		return ""
	}
	return s
}

// schemaRemovals compares current (folded from the log) against planned
// (folded from the file's own full compiled sequence, per
// SchemaFromEnvelopes) and compiled (schemasrc.Compile's raw envelopes for
// the same file) and reports every declaration, and now every field
// attribute, the file would remove: a missing type, op, or field; a
// description present in the log but absent from the file; a deprecation
// the file would silently clear; or one of schemaFieldAttributeKeys the
// log's folded state carries for a field the file still declares, whose
// compiled define-field body no longer carries that key. None of these are
// representable in the log — nothing is ever removed, no op clears
// `deprecated`, and no op clears a single attribute — so each is refused
// rather than silently compiled into a no-op, a tombstone, or (the
// attribute case) a define-field op that folds against the log's real
// history to a state the file itself no longer describes.
//
// resolveSchemaTarget picks the target object by matching f.Namespace
// against the namespaces of the schema objects already in the log, not by
// continuity with a prior apply's object id. A namespace the log has not
// seen before matches no schema object, so resolveSchemaTarget mints a
// fresh object id and current is the zero state.Schema{} for that apply —
// there is nothing to compare planned against. A namespace the log
// already holds — including one an earlier edit moved away from and this
// one moves back to — matches that object and reuses it, so current is
// whatever the log folds to for it, non-zero, and the comparison below
// runs normally.
//
// Comparison is always compiled declarations (planned, compiled) against
// folded state (current), never source text — comments, spacing, and
// declaration order in the file have no bearing on this check, by
// construction.
func schemaRemovals(current, planned state.Schema, compiled []codec.Envelope) ([]string, error) {
	var problems []string

	if current.Description != "" && planned.Description == "" {
		problems = append(problems, "the schema object's description was removed; mark it differently or leave it in place, nothing is ever removed from the log")
	}

	for _, ct := range current.Types {
		pt, ok := findSchemaType(planned, ct.Name)
		if !ok {
			problems = append(problems, fmt.Sprintf("type %q was removed; mark it `deprecated` instead", ct.Name))
			continue
		}
		if ct.Deprecated && !pt.Deprecated {
			problems = append(problems, fmt.Sprintf("type %q was un-deprecated; no op can clear a deprecation once written", ct.Name))
		}
		if ct.Description != "" && pt.Description == "" {
			problems = append(problems, fmt.Sprintf("type %q's description was removed", ct.Name))
		}

		for _, co := range ct.Ops {
			po, ok := findSchemaOp(pt, co.OpType, co.OpVersion)
			if !ok {
				problems = append(problems, fmt.Sprintf("op %s version %d on type %q was removed", co.OpType, co.OpVersion, ct.Name))
				continue
			}
			if co.Description != "" && po.Description == "" {
				problems = append(problems, fmt.Sprintf("op %s version %d on type %q's description was removed", co.OpType, co.OpVersion, ct.Name))
			}
		}

		for _, cf := range ct.Fields {
			pf, ok := findSchemaField(pt, cf.OpType, cf.OpVersion, cf.Name)
			if !ok {
				problems = append(problems, fmt.Sprintf("field %q on op %s version %d of type %q was removed; mark it `deprecated` instead", cf.Name, cf.OpType, cf.OpVersion, ct.Name))
				continue
			}
			if cf.Deprecated && !pf.Deprecated {
				problems = append(problems, fmt.Sprintf("field %q on op %s version %d of type %q was un-deprecated; no op can clear a deprecation once written", cf.Name, cf.OpType, cf.OpVersion, ct.Name))
			}

			body, found, err := compiledFieldBody(compiled, ct.Name, cf.OpType, cf.OpVersion, cf.Name)
			if err != nil {
				return nil, err
			}
			if !found {
				// The field is still declared (pf, above, was found), so
				// compiled must carry a define-field envelope for it —
				// schemasrc.Compile emits exactly one per currently
				// declared field. Not finding one here means compiled and
				// planned disagree about what the file declares, which
				// would be a schemasrc.Compile bug, not a narrowing;
				// nothing to refuse.
				continue
			}
			for _, attr := range schemaFieldAttributeKeys {
				if !schemaFieldHasAttribute(cf, attr) {
					continue
				}
				if _, present := body[attr]; !present {
					problems = append(problems, fmt.Sprintf(
						"field %q on op %s version %d of type %q: attribute %q was removed; nothing is ever removed from the log — %s (spec/schema-ops.md §8.1)",
						cf.Name, cf.OpType, cf.OpVersion, ct.Name, attr, schemaAttributeNarrowingAdvice(attr)))
				}
			}
		}
	}

	return problems, nil
}

// Typed op bodies, matching spec/schema-ops.md §4 exactly, for reading
// values back out of a compiled envelope's canonical JSON — the delta
// computation's only use for them.
type schemaCreateBody struct {
	Namespace   string `json:"namespace"`
	Description string `json:"description,omitempty"`
}
type schemaDefineTypeBody struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}
type schemaDefineOpBody struct {
	Type        string `json:"type"`
	OpType      string `json:"op_type"`
	OpVersion   string `json:"op_version"`
	Description string `json:"description,omitempty"`
}
type schemaDefineFieldBody struct {
	Type      string            `json:"type"`
	OpType    string            `json:"op_type"`
	OpVersion string            `json:"op_version"`
	Field     string            `json:"field"`
	ValueType string            `json:"value_type,omitempty"`
	Enum      []string          `json:"enum,omitempty"`
	MaxLength int64             `json:"max_length,omitempty"`
	Strategy  string            `json:"strategy"`
	Key       []string          `json:"key,omitempty"`
	KeyTypes  map[string]string `json:"key_types,omitempty"`
	Lattice   []string          `json:"lattice,omitempty"`
	Target    string            `json:"target,omitempty"`
}
type schemaDeprecateTypeBody struct {
	Type       string `json:"type"`
	Deprecated bool   `json:"deprecated"`
}
type schemaDeprecateFieldBody struct {
	Type       string `json:"type"`
	OpType     string `json:"op_type"`
	OpVersion  string `json:"op_version"`
	Field      string `json:"field"`
	Deprecated bool   `json:"deprecated"`
}

// schemaDelta walks compiled (schemasrc.Compile's canonical order) and
// keeps only the envelopes current does not already reflect — the result
// is a subsequence of the canonical order, so applying it twice is a no-op
// (WRIT-191's central idempotence property).
func schemaDelta(current state.Schema, compiled []codec.Envelope) ([]codec.Envelope, error) {
	var delta []codec.Envelope
	for _, env := range compiled {
		keep, err := schemaDeltaKeep(current, env)
		if err != nil {
			return nil, err
		}
		if keep {
			delta = append(delta, env)
		}
	}
	return delta, nil
}

func schemaDeltaKeep(current state.Schema, env codec.Envelope) (bool, error) {
	switch env.OpType {
	case "create":
		var b schemaCreateBody
		if err := json.Unmarshal(env.Body, &b); err != nil {
			return false, err
		}
		if current.ObjectID == "" {
			return true, nil
		}
		return current.Description != b.Description, nil

	case "define-type":
		var b schemaDefineTypeBody
		if err := json.Unmarshal(env.Body, &b); err != nil {
			return false, err
		}
		t, ok := findSchemaType(current, b.Type)
		if !ok {
			return true, nil
		}
		return t.Description != b.Description, nil

	case "define-op":
		var b schemaDefineOpBody
		if err := json.Unmarshal(env.Body, &b); err != nil {
			return false, err
		}
		ver, err := strconv.ParseInt(b.OpVersion, 10, 64)
		if err != nil {
			return false, fmt.Errorf("define-op op_version %q: %w", b.OpVersion, err)
		}
		t, ok := findSchemaType(current, b.Type)
		if !ok {
			return true, nil
		}
		o, ok := findSchemaOp(t, b.OpType, ver)
		if !ok {
			return true, nil
		}
		return o.Description != b.Description, nil

	case "define-field":
		var b schemaDefineFieldBody
		if err := json.Unmarshal(env.Body, &b); err != nil {
			return false, err
		}
		ver, err := strconv.ParseInt(b.OpVersion, 10, 64)
		if err != nil {
			return false, fmt.Errorf("define-field op_version %q: %w", b.OpVersion, err)
		}
		t, ok := findSchemaType(current, b.Type)
		if !ok {
			return true, nil
		}
		fl, ok := findSchemaField(t, b.OpType, ver, b.Field)
		if !ok {
			return true, nil
		}
		return !schemaFieldMatchesBody(fl, b), nil

	case "deprecate-type":
		var b schemaDeprecateTypeBody
		if err := json.Unmarshal(env.Body, &b); err != nil {
			return false, err
		}
		t, ok := findSchemaType(current, b.Type)
		if !ok {
			return true, nil
		}
		return !t.Deprecated, nil

	case "deprecate-field":
		var b schemaDeprecateFieldBody
		if err := json.Unmarshal(env.Body, &b); err != nil {
			return false, err
		}
		ver, err := strconv.ParseInt(b.OpVersion, 10, 64)
		if err != nil {
			return false, fmt.Errorf("deprecate-field op_version %q: %w", b.OpVersion, err)
		}
		t, ok := findSchemaType(current, b.Type)
		if !ok {
			return true, nil
		}
		fl, ok := findSchemaField(t, b.OpType, ver, b.Field)
		if !ok {
			return true, nil
		}
		return !fl.Deprecated, nil

	default:
		// schemasrc.Compile never emits anything else; kept anyway, and
		// conservatively, so an unknown op type is never silently dropped
		// (forward-compatibility, spec/schema-ops.md §10).
		return true, nil
	}
}

func schemaFieldMatchesBody(f state.SchemaField, b schemaDefineFieldBody) bool {
	if f.ValueType != b.ValueType {
		return false
	}
	if !stringSlicesEqual(f.Enum, b.Enum) {
		return false
	}
	if f.MaxLength != b.MaxLength {
		return false
	}
	if f.Strategy != b.Strategy {
		return false
	}
	if !stringSlicesEqual(f.Key, b.Key) {
		return false
	}
	if !stringMapsEqual(f.KeyTypes, b.KeyTypes) {
		return false
	}
	if !stringSlicesEqual(f.Lattice, b.Lattice) {
		return false
	}
	if f.Target != b.Target {
		return false
	}
	return true
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// renderSchemaPlanPorcelain writes plan's human-readable output: a unified
// diff of the current-vs-planned renderings (both folded state, never
// source text), a one-line op-count summary, and the next step.
func renderSchemaPlanPorcelain(w io.Writer, r *schemaPlanResult) {
	// c.Reason can name a value straight out of a foreign client's
	// non-conforming op body, with no repertoire gate at all: a
	// define-field's field, for instance, on the path
	// spec.ValidateFieldRule has just refused it over --
	// engine/schema.go's SchemaConflict construction for that case formats
	// the field verbatim into Reason to explain the rejection, not to
	// re-validate it. This is a rendering chokepoint exactly like
	// fieldDisplay/authorDisplay and needs the same escape (round 4 review
	// of PR #185, finding 1).
	for _, c := range r.conflicts {
		fmt.Fprintf(w, "conflict: %s\n", textsafe.EscapeForbidden(c.Reason))
	}

	if r.upToDate {
		fmt.Fprintln(w, "writ.schema matches the schema in the log.")
		return
	}

	// escapeRenderedSchemaSource runs on a local copy of r.currentSource
	// and r.plannedSource here, not on those fields themselves and not
	// before schemasrc.Render assembles them -- see buildSchemaPlan's
	// comment for why, and escapeRenderedSchemaSource's own comment for
	// why running after Render is what keeps this both correct and
	// injective (round 4 review of PR #185, finding 2).
	diff := textdiff.DiffText(
		"schema in the log", escapeRenderedSchemaSource(r.currentSource),
		"writ.schema", escapeRenderedSchemaSource(r.plannedSource),
	)
	if diff != "" {
		fmt.Fprint(w, diff)
	}

	counts := make(map[string]int)
	var order []string
	for _, op := range r.ops {
		if counts[op.OpType] == 0 {
			order = append(order, op.OpType)
		}
		counts[op.OpType]++
	}
	var parts []string
	for _, opType := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[opType], opType))
	}
	if r.created {
		fmt.Fprintf(w, "%d op(s) to append (will create a new schema object): %s\n", len(r.ops), strings.Join(parts, ", "))
	} else {
		fmt.Fprintf(w, "%d op(s) to append: %s\n", len(r.ops), strings.Join(parts, ", "))
	}
	fmt.Fprintln(w, "run `writ schema apply` to sign and append them")
}

// escapeRenderedSchemaSource returns src -- schemasrc.Render's output --
// with every textsafe.Forbidden code point escaped as \uXXXX, except
// U+000A. It exists only for renderSchemaPlanPorcelain's local diff-display
// copy of currentSource/plannedSource: buildSchemaPlan calls Render on the
// unescaped schema deliberately, so r.currentSource and r.plannedSource
// themselves -- what toWirePlan hands emitJSON for --json -- stay Render's
// raw, faithful output (docs/cli-json.md's promise for current_source and
// planned_source). This function's job is display-only, for the one path
// (the human-readable diff) with no JSON layer underneath to fall back on
// for its own escaping.
//
// Running after Render, not before, is what round 4's finding 2 on PR #185
// requires. An earlier version of this fix escaped each description before
// handing it to Render, which made schemasrc.quoteString backslash-escape
// the literal '\' that escape had just introduced: the rendered text came
// out double-escaped, and -- because two different descriptions could
// collide onto the same double-escaped text -- lost injectivity, so a real
// description change could diff as empty. Escaping the already-rendered,
// already-quoted output introduces no new backslash for anything
// downstream to re-escape, and nothing runs after this to corrupt it
// either.
//
// Excluding U+000A is what keeps the diff line-structured. quoteString
// already turns every data newline inside a description into the literal
// two characters `\n` (its own case '\n'), so by the time Render's output
// reaches here, the only raw U+000A bytes left are the real line breaks
// Render's own Fprintf format strings write between declarations --
// structure, not data. Escaping those too, as WRIT-137 round 2 once did
// over the whole assembled document, collapses the diff onto one line for
// every schema, benign or hostile (round 3 review of PR #185, finding 1).
// No other Forbidden code point can occur structurally -- every literal
// byte Render itself writes is plain ASCII -- so U+000A is the only
// exclusion needed, not one case among several still to find.
//
// The result is injective. A description whose literal text already
// spells out the six ASCII characters backslash, u, 2, 0, 2, e (ordinary
// text, no Forbidden rune among them) quotes to two backslashes followed
// by u202e: quoteString escapes the data backslash, and this pass leaves
// the result alone, since backslash is not Forbidden. A description
// containing the actual U+202E code point quotes to a raw rune
// (quoteString's default case passes it through unescaped), which this
// pass turns into a single backslash followed by u202e. One backslash
// versus two keeps the renderings visibly distinct.
func escapeRenderedSchemaSource(src []byte) string {
	s := string(src)
	hasForbidden := strings.ContainsFunc(s, func(r rune) bool {
		return r != '\n' && textsafe.Forbidden(r)
	})
	if !hasForbidden {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || !textsafe.Forbidden(r) {
			b.WriteRune(r)
			continue
		}
		fmt.Fprintf(&b, `\u%04x`, r)
	}
	return b.String()
}
