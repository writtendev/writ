// Package wire defines the CLI-owned JSON wire format for writ plumbing mode (--json).
//
// These wire types are deliberately decoupled from domain serialization and engine state tags
// so that internal engine changes cannot silently break scripted consumers.
package wire

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
)

// CurrentSchemaVersion is the version of the JSON plumbing envelope schema.
const CurrentSchemaVersion = 1

// Envelope kinds for plumbing commands.
const (
	KindSyncStatus   = "sync.status"
	KindSyncResult   = "sync.result"
	KindSchemaPlan   = "schema.plan"
	KindSchemaApply  = "schema.apply"
	KindSchemaShow   = "schema.show"
	KindObjectCreate = "object.create"
	KindObjectApply  = "object.apply"
	KindObjectShow   = "object.show"
	KindObjectList   = "object.list"
)

// Envelope wraps all machine-readable output in a single versioned container.
type Envelope struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Data          any    `json:"data"`
}

// Author represents the display name and email address of an author.
type Author struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// UnknownOp records an unrecognized operation preserved for forward compatibility.
type UnknownOp struct {
	Commit     string `json:"commit"`
	ObjectType string `json:"object_type"`
	OpType     string `json:"op_type"`
	OpVersion  int64  `json:"op_version"`
}

// Failure represents structured error reporting for a failed sync operation.
type Failure struct {
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Advice    string `json:"advice,omitempty"`
	Retryable bool   `json:"retryable"`
}

// SyncStatus reports synchronization status for a single remote.
type SyncStatus struct {
	Remote   string   `json:"remote"`
	Unsynced int      `json:"unsynced"`
	Failure  *Failure `json:"failure,omitempty"`
}

// SyncResult reports aggregate statistics from a sync operation for a single remote.
type SyncResult struct {
	Remote         string   `json:"remote"`
	OpsFetched     int      `json:"ops_fetched"`
	OpsPushed      int      `json:"ops_pushed"`
	ObjectsTouched int      `json:"objects_touched"`
	Unsynced       int      `json:"unsynced"`
	Failure        *Failure `json:"failure,omitempty"`
}

// FromSyncStatus converts a writ.SyncStatus into a SyncStatus wire struct.
func FromSyncStatus(s writ.SyncStatus) SyncStatus {
	return SyncStatus{
		Remote:   s.Remote,
		Unsynced: s.Unsynced,
	}
}

// FromSyncStatusFailure converts a remote name, error, and unsynced count into a SyncStatus wire struct with Failure populated.
func FromSyncStatusFailure(remote string, err error, unsynced int) SyncStatus {
	var failure *Failure
	if err != nil {
		var syncErr *writ.SyncError
		if errors.As(err, &syncErr) {
			failure = &Failure{
				Kind:      syncErr.Kind,
				Message:   syncErr.Message,
				Advice:    syncErr.Advice,
				Retryable: syncErr.Retryable,
			}
			unsynced = syncErr.Unsynced
		} else {
			failure = &Failure{
				Kind:      "unknown",
				Message:   err.Error(),
				Retryable: false,
			}
		}
	}
	return SyncStatus{
		Remote:   remote,
		Unsynced: unsynced,
		Failure:  failure,
	}
}

// FromSyncResult converts a remote name and writ.SyncResult into a SyncResult wire struct.
func FromSyncResult(remote string, res writ.SyncResult) SyncResult {
	return SyncResult{
		Remote:         remote,
		OpsFetched:     res.OpsFetched,
		OpsPushed:      res.OpsPushed,
		ObjectsTouched: res.ObjectsTouched,
		Unsynced:       res.Unsynced,
	}
}

// FromSyncResultFailure converts a remote name, writ.SyncResult, and error into a SyncResult wire struct with Failure populated.
func FromSyncResultFailure(remote string, res writ.SyncResult, err error) SyncResult {
	var failure *Failure
	unsynced := res.Unsynced
	if err != nil {
		var syncErr *writ.SyncError
		if errors.As(err, &syncErr) {
			failure = &Failure{
				Kind:      syncErr.Kind,
				Message:   syncErr.Message,
				Advice:    syncErr.Advice,
				Retryable: syncErr.Retryable,
			}
			unsynced = syncErr.Unsynced
		} else {
			failure = &Failure{
				Kind:      "unknown",
				Message:   err.Error(),
				Retryable: false,
			}
		}
	}
	return SyncResult{
		Remote:         remote,
		OpsFetched:     res.OpsFetched,
		OpsPushed:      res.OpsPushed,
		ObjectsTouched: res.ObjectsTouched,
		Unsynced:       unsynced,
		Failure:        failure,
	}
}

// SchemaOpEntry is one op `writ schema plan`/`apply` would append (or did),
// in the same op vocabulary spec/schema-ops.md defines. Body is the
// envelope body verbatim — the normative wire form is the honest answer to
// "which ops would be appended", stable in a way a re-modelled shape would
// not be.
type SchemaOpEntry struct {
	OpType string          `json:"op_type"`
	Body   json.RawMessage `json:"body"`
}

// FromSchemaEnvelopes converts a compiled op sequence into wire entries,
// preserving order. Collections are always non-nil so they serialize as `[]`.
func FromSchemaEnvelopes(envs []codec.Envelope) []SchemaOpEntry {
	out := make([]SchemaOpEntry, len(envs))
	for i, e := range envs {
		out[i] = SchemaOpEntry{OpType: e.OpType, Body: json.RawMessage(e.Body)}
	}
	return out
}

// SchemaConflict is a load-bearing collision between schema objects
// (spec/schema-ops.md §Conflicts), reported by `plan` whether or not the
// working-tree file caused it — it is the only surface that shows them.
type SchemaConflict struct {
	ObjectType string   `json:"object_type,omitempty"`
	Namespace  string   `json:"namespace,omitempty"`
	ObjectIDs  []string `json:"object_ids"`
	Reason     string   `json:"reason"`
}

// FromSchemaConflicts converts domain SchemaConflicts to wire form.
// Collections are always non-nil so they serialize as `[]`.
func FromSchemaConflicts(conflicts []writ.SchemaConflict) []SchemaConflict {
	out := make([]SchemaConflict, len(conflicts))
	for i, c := range conflicts {
		ids := c.ObjectIDs
		if ids == nil {
			ids = []string{}
		}
		out[i] = SchemaConflict{ObjectType: c.ObjectType, Namespace: c.Namespace, ObjectIDs: ids, Reason: c.Reason}
	}
	return out
}

// SchemaPlan is the `schema.plan` JSON payload: the target schema object
// `writ schema apply` would write to, whether it would be minted fresh, and
// the ops that would be appended to bring it in line with the working-tree
// writ.schema file.
//
// ObjectID is always present. A creation plan's id used to be a preview
// only — apply resolved its own target independently and minted its own
// id, so the two could diverge — but the schema object id is now derived
// from the namespace (spec/identifiers.md's schema carve-out), so plan's
// id for a fresh object is the exact id apply would write to.
type SchemaPlan struct {
	ObjectID      string           `json:"object_id"`
	Namespace     string           `json:"namespace"`
	Created       bool             `json:"created"`
	UpToDate      bool             `json:"up_to_date"`
	Ops           []SchemaOpEntry  `json:"ops"`
	CurrentSource string           `json:"current_source"`
	PlannedSource string           `json:"planned_source"`
	Conflicts     []SchemaConflict `json:"conflicts"`
}

// SchemaApply is the `schema.apply` JSON payload: the target schema object
// written to, and the ops actually appended.
//
// Namespaces is the repository's distinct, sorted namespace set as it
// stands after this apply -- the fact that lets a scripted caller detect
// "this apply created a new schema object" and read the resulting count
// (`.data.namespaces | length`) without parsing porcelain prose (WRIT-223).
// Always non-nil so it serializes as `[]`, never `null`.
type SchemaApply struct {
	ObjectID    string          `json:"object_id"`
	Namespace   string          `json:"namespace"`
	Namespaces  []string        `json:"namespaces"`
	Created     bool            `json:"created"`
	OpsAppended int             `json:"ops_appended"`
	Ops         []SchemaOpEntry `json:"ops"`
}

// FromUnknownOps converts unknown ops to wire form, preserving order.
// Collections are always non-nil so they serialize as `[]`.
func FromUnknownOps(ops []writ.UnknownOp) []UnknownOp {
	out := make([]UnknownOp, len(ops))
	for i, u := range ops {
		out[i] = UnknownOp{Commit: u.Commit, ObjectType: u.ObjectType, OpType: u.OpType, OpVersion: u.OpVersion}
	}
	return out
}

// ObjectCreated is the `object.create` JSON payload.
type ObjectCreated struct {
	ObjectID   string `json:"object_id"`
	ObjectType string `json:"object_type"`
}

// ObjectApplied is the `object.apply` JSON payload.
type ObjectApplied struct {
	ObjectID string `json:"object_id"`
	OpType   string `json:"op_type"`
}

// Object is the `object.show` JSON payload: the folded state of one
// collaborative object of any schema-declared type. Fields is an OPEN MAP
// KEYED BY TARGET KEY, not a fixed field set -- the one place this wire
// format's usual additive-only, never-retyped promise needs a
// schema-shaped carve-out, because the object type it describes is data,
// not a Go struct this package ships.
type Object struct {
	ObjectID   string         `json:"object_id"`
	ObjectType string         `json:"object_type"`
	Fields     map[string]any `json:"fields"`
	UnknownOps []UnknownOp    `json:"unknown_ops"`
}

// FromObject converts a folded writ.Object to wire form.
func FromObject(o writ.Object) Object {
	fields := o.Fields
	if fields == nil {
		fields = map[string]any{}
	}
	return Object{
		ObjectID:   o.ObjectID,
		ObjectType: o.ObjectType,
		Fields:     fields,
		UnknownOps: FromUnknownOps(o.UnknownOps),
	}
}

// ObjectSummary is a single row in the `object list` cross-type output.
// It carries no op id: ARCHITECTURE.md §Public API shape keeps the wire
// layer schema-shaped, never git-shaped -- callers see no SHAs unless they
// ask, and a list row is not asking. op_count is fine, since it is a count,
// not an identifier.
type ObjectSummary struct {
	ObjectID   string    `json:"object_id"`
	ObjectType string    `json:"object_type"`
	Author     Author    `json:"author"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	OpCount    int       `json:"op_count"`
}

// FromObjectResultSummary converts one cross-type object query row to wire form.
func FromObjectResultSummary(r writ.ObjectResult) ObjectSummary {
	return ObjectSummary{
		ObjectID:   r.ObjectID,
		ObjectType: r.ObjectType,
		Author:     Author{Name: r.Author.Name, Email: r.Author.Email},
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
		OpCount:    r.OpCount,
	}
}

// FromObjectResultSummaries converts cross-type object query rows to wire
// form. Collections are always non-nil so they serialize as `[]`.
func FromObjectResultSummaries(results []writ.ObjectResult) []ObjectSummary {
	out := make([]ObjectSummary, len(results))
	for i, r := range results {
		out[i] = FromObjectResultSummary(r)
	}
	return out
}

// SchemaTypeField is the wire form of one field declaration within a
// schema-declared type's op vocabulary (`schema show`).
type SchemaTypeField struct {
	Name       string            `json:"field"`
	OpType     string            `json:"op_type"`
	OpVersion  int64             `json:"op_version"`
	ValueType  string            `json:"value_type,omitempty"`
	Enum       []string          `json:"enum,omitempty"`
	MaxLength  int64             `json:"max_length,omitempty"`
	Strategy   string            `json:"strategy,omitempty"`
	Key        []string          `json:"key,omitempty"`
	KeyTypes   map[string]string `json:"key_types,omitempty"`
	Lattice    []string          `json:"lattice,omitempty"`
	Target     string            `json:"target,omitempty"`
	Deprecated bool              `json:"deprecated,omitempty"`
}

// SchemaTypeOp is the wire form of one op type declared within a
// schema-declared type's vocabulary (`schema show`).
type SchemaTypeOp struct {
	OpType      string `json:"op_type"`
	OpVersion   int64  `json:"op_version"`
	Description string `json:"description,omitempty"`
}

// SchemaTypeInfo is the `schema.show` JSON payload for one object type: the
// vocabulary installed and folding right now (Store.Types), not what
// `writ schema plan`/`apply` would write (Store.Schema) -- see the
// command's own Long text.
type SchemaTypeInfo struct {
	Name        string            `json:"type"`
	Description string            `json:"description,omitempty"`
	Deprecated  bool              `json:"deprecated,omitempty"`
	Fields      []SchemaTypeField `json:"fields,omitempty"`
	Ops         []SchemaTypeOp    `json:"ops,omitempty"`
}

// FromSchemaTypeInfo converts one Store.Types entry to wire form. Fields
// and Ops are built as non-nil (possibly empty) slices, but -- unlike this
// file's other From* converters -- SchemaTypeInfo declares both
// `omitempty`, so a field-less or op-less type's empty slice is omitted
// from the wire output entirely rather than serializing as `[]`. See
// docs/cli-json.md's schema.show table ("Omitted when empty").
func FromSchemaTypeInfo(t writ.SchemaType) SchemaTypeInfo {
	fields := make([]SchemaTypeField, len(t.Fields))
	for i, f := range t.Fields {
		fields[i] = SchemaTypeField{
			Name:       f.Name,
			OpType:     f.OpType,
			OpVersion:  f.OpVersion,
			ValueType:  f.ValueType,
			Enum:       f.Enum,
			MaxLength:  f.MaxLength,
			Strategy:   f.Strategy,
			Key:        f.Key,
			KeyTypes:   f.KeyTypes,
			Lattice:    f.Lattice,
			Target:     f.Target,
			Deprecated: f.Deprecated,
		}
	}
	ops := make([]SchemaTypeOp, len(t.Ops))
	for i, o := range t.Ops {
		ops[i] = SchemaTypeOp{OpType: o.OpType, OpVersion: o.OpVersion, Description: o.Description}
	}
	return SchemaTypeInfo{
		Name:        t.Name,
		Description: t.Description,
		Deprecated:  t.Deprecated,
		Fields:      fields,
		Ops:         ops,
	}
}

// FromSchemaTypeInfos converts Store.Types' result to wire form. Each
// entry's Fields/Ops omit when empty -- see FromSchemaTypeInfo.
func FromSchemaTypeInfos(types []writ.SchemaType) []SchemaTypeInfo {
	out := make([]SchemaTypeInfo, len(types))
	for i, t := range types {
		out[i] = FromSchemaTypeInfo(t)
	}
	return out
}
