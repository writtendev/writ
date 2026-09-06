package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
			objectID:  planRes.objectID,
			namespace: planRes.namespace,
			created:   planRes.created,
			ops:       planRes.ops,
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
	objectID  string
	namespace string
	created   bool
	ops       []codec.Envelope
}

func (r schemaApplyResult) toWireApply() wire.SchemaApply {
	return wire.SchemaApply{
		ObjectID:    r.objectID,
		Namespace:   r.namespace,
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

	if problems := schemaRemovals(current, planned); len(problems) > 0 {
		msgs := make([]string, 0, len(problems)+1)
		msgs = append(msgs, "writ schema: refusing to plan (nothing is ever removed from the log):")
		for _, p := range problems {
			msgs = append(msgs, "  - "+p)
		}
		return nil, &schemaError{msgs: msgs}
	}

	delta, err := schemaDelta(current, compiled)
	if err != nil {
		return nil, fmt.Errorf("writ schema: %w", err)
	}

	// A brand-new target has no rendering of its own yet — current is the
	// zero Schema{}, and Render requires a namespace matching
	// namespacePattern, which "" never does. Render only what actually
	// exists in the log; an empty current_source reads correctly as a diff
	// against nothing, which is exactly what creating an object is.
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
		created:       created,
		upToDate:      len(delta) == 0,
		ops:           delta,
		currentSource: currentSource,
		plannedSource: plannedSource,
		conflicts:     conflicts,
	}, nil
}

// resolveSchemaTarget implements the one decision this ticket owns: which
// schema object `apply` writes to (WRIT-191 plan, "Which schema object
// apply writes to"). Computed fresh on every run — there is no plan
// artifact and no recorded id, and no --object-id flag: every case that
// flag would serve is a repository already in the state this guard exists
// to prevent.
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
		declared := make(map[string]bool, len(f.Types))
		for _, t := range f.Types {
			declared[t.Name] = true
		}
		contestedOwner := make(map[string]string)
		for _, s := range schemas {
			for _, t := range s.Types {
				if declared[t.Name] {
					contestedOwner[t.Name] = s.ObjectID
				}
			}
		}
		if len(contestedOwner) > 0 {
			var names []string
			for name := range contestedOwner {
				names = append(names, name)
			}
			sort.Strings(names)
			var parts []string
			for _, name := range names {
				parts = append(parts, fmt.Sprintf("%q (already bound by schema object %s)", name, contestedOwner[name]))
			}
			return "", fmt.Errorf(
				"writ schema: no schema object declares namespace %q, but this file would bind object_type(s) %s to a new schema object; applying would bind the same object_type twice and RulesFromSchemas withholds all rules for it, permanently — reconcile the namespace or type name before running `writ schema apply`",
				f.Namespace, strings.Join(parts, ", "))
		}
		id, err := newSchemaObjectID()
		if err != nil {
			return "", fmt.Errorf("writ schema: mint schema object id: %w", err)
		}
		return id, nil
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

// newSchemaObjectID mints a fresh object id per spec/identifiers.md: 128
// bits of CSPRNG randomness, rendered as 32 lowercase hex characters. This
// mirrors engine/objectid.go's unexported newObjectID exactly — object id
// minting for a brand-new schema object happens here, in the CLI, rather
// than in the engine, because schemasrc.Compile needs the target id before
// the engine ever sees a single envelope: there is no "create and get an
// id back" call for schema the way Reviews.Create or Issues.Create offer
// for their own types.
func newSchemaObjectID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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

// schemaRemovals compares current (folded from the log) against planned
// (folded from the file's own full compiled sequence, per
// SchemaFromEnvelopes) and reports every declaration the file would remove:
// a missing type, op, or field; a description present in the log but
// absent from the file; or a deprecation the file would silently clear.
// None of these are representable in the log — nothing is ever removed,
// and no op clears `deprecated` — so each is refused rather than silently
// compiled into a no-op or a tombstone the caller didn't ask for.
//
// Comparison is always compiled declarations (planned) against folded
// state (current), never source text — comments, spacing, and declaration
// order in the file have no bearing on this check, by construction.
func schemaRemovals(current, planned state.Schema) []string {
	var problems []string

	if current.Namespace != "" && current.Namespace != planned.Namespace {
		problems = append(problems, fmt.Sprintf(
			"namespace changed from %q to %q; a schema object's namespace is set once by its first create op and never changes",
			current.Namespace, planned.Namespace))
	}
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
		}
	}

	return problems
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
	for _, c := range r.conflicts {
		fmt.Fprintf(w, "conflict: %s\n", c.Reason)
	}

	if r.upToDate {
		fmt.Fprintln(w, "writ.schema matches the schema in the log.")
		return
	}

	diff := textdiff.DiffText("schema in the log", string(r.currentSource), "writ.schema", string(r.plannedSource))
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
		fmt.Fprintf(w, "%d op(s) to append (will create schema object %s): %s\n", len(r.ops), r.objectID, strings.Join(parts, ", "))
	} else {
		fmt.Fprintf(w, "%d op(s) to append: %s\n", len(r.ops), strings.Join(parts, ", "))
	}
	fmt.Fprintln(w, "run `writ schema apply` to sign and append them")
}
