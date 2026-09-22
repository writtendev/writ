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
	KindInitResult   = "init.result"
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
	Commit       string `json:"commit"`
	ObjectType   string `json:"object_type"`
	OpType       string `json:"op_type"`
	OpVersion    int64  `json:"op_version"`
	Verification string `json:"verification"`
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
	Rejected       int      `json:"rejected,omitempty"`
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
		Rejected:       res.Rejected,
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
		Rejected:       res.Rejected,
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
func FromSchemaEnvelopes(envs []writ.Envelope) []SchemaOpEntry {
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
		out[i] = UnknownOp{Commit: u.Commit, ObjectType: u.ObjectType, OpType: u.OpType, OpVersion: u.OpVersion, Verification: u.Verification}
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
	ObjectID     string         `json:"object_id"`
	ObjectType   string         `json:"object_type"`
	Fields       map[string]any `json:"fields"`
	UnknownOps   []UnknownOp    `json:"unknown_ops"`
	Verification string         `json:"verification"`
}

// FromObject converts a folded writ.Object to wire form.
func FromObject(o writ.Object) Object {
	fields := o.Fields
	if fields == nil {
		fields = map[string]any{}
	}
	return Object{
		ObjectID:     o.ObjectID,
		ObjectType:   o.ObjectType,
		Fields:       fields,
		UnknownOps:   FromUnknownOps(o.UnknownOps),
		Verification: o.Verification,
	}
}

// rfc3339Min and rfc3339Max are the earliest and latest instants Go's
// strict RFC 3339 encoder (time.Time.MarshalJSON, appendStrictRFC3339)
// will accept -- the boundaries of the four-digit year field RFC 3339
// requires. Anything outside this range fails to marshal at all.
//
// rfc3339MinUnix/rfc3339MaxUnix are those same bounds as epoch seconds --
// see clampRFC3339 for why the comparison must happen in that form.
var (
	rfc3339Min = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
	rfc3339Max = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

	rfc3339MinUnix = rfc3339Min.Unix()
	rfc3339MaxUnix = rfc3339Max.Unix()
)

// clampRFC3339 pins t into the RFC 3339-representable range [rfc3339Min,
// rfc3339Max], returning the clamped time and, only when a clamp actually
// fired, the true value's epoch seconds (nil otherwise).
//
// This exists because an object's created_at/updated_at ultimately derive
// from a peer's commit author timestamp, which is unbounded by design:
// git and go-git accept any signed 64-bit second count, and spec/fold.md
// §3-4's causality-monotone total order sorts on that exact value via
// engine/internal/fold/order.go's readyHeap.Less. Clamping anywhere at or
// below the engine would change which value the fold and the projection
// cache see, and therefore change fold output for the affected ops -- a
// normative change to spec/fold.md, not a rendering concern. So the clamp
// lives here, at the last renderer, and must never travel back up into
// the engine: this package's job is exactly to keep engine-side facts
// from breaking a scripted consumer's JSON decoder (see the package doc
// comment above), and an unmarshalable timestamp is precisely that kind
// of break.
//
// The comparison is done on t.Unix() (raw epoch seconds), never on t
// itself via Before/After. time.Unix(sec, 0) builds its absolute internal
// representation as sec + a fixed constant (seconds from year 1 to the
// Unix epoch); for a sec near either end of the int64 range that addition
// overflows int64 and wraps, so Before/After -- which compare that
// wrapped absolute representation -- can come out backwards (a
// far-future sec reads as "before year 0"). t.Unix() is unaffected: it
// reverses that same addition, and addition/subtraction by a fixed
// constant are exact inverses under two's-complement wraparound, so it
// always recovers the original sec bit-for-bit even when the
// intermediate overflowed. Comparing sec against the bounds' own Unix
// seconds sidesteps the overflow entirely and picks the correct bound.
func clampRFC3339(t time.Time) (time.Time, *int64) {
	sec := t.Unix()
	switch {
	case sec < rfc3339MinUnix:
		epoch := sec
		return rfc3339Min, &epoch
	case sec > rfc3339MaxUnix:
		epoch := sec
		return rfc3339Max, &epoch
	default:
		return t, nil
	}
}

// ObjectSummary is a single row in the `object list` cross-type output.
// It carries no op id: ARCHITECTURE.md §Public API shape keeps the wire
// layer schema-shaped, never git-shaped -- callers see no SHAs unless they
// ask, and a list row is not asking. op_count is fine, since it is a count,
// not an identifier.
//
// CreatedAt/UpdatedAt are clamped to the RFC 3339-representable range
// (see clampRFC3339): a hostile or malformed peer's out-of-range author
// timestamp must not take down --json's entire output document just
// because time.Time.MarshalJSON refuses to encode it. CreatedAtEpoch/
// UpdatedAtEpoch carry the true epoch seconds when (and only when) a
// clamp fired, so the value is never silently fabricated -- their
// presence is itself the out-of-range signal, deliberately with no
// separate boolean flag.
type ObjectSummary struct {
	ObjectID       string    `json:"object_id"`
	ObjectType     string    `json:"object_type"`
	Author         Author    `json:"author"`
	CreatedAt      time.Time `json:"created_at"`
	CreatedAtEpoch *int64    `json:"created_at_epoch,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
	UpdatedAtEpoch *int64    `json:"updated_at_epoch,omitempty"`
	OpCount        int       `json:"op_count"`
	Verification   string    `json:"verification"`
}

// FromObjectResultSummary converts one cross-type object query row to wire form.
func FromObjectResultSummary(r writ.ObjectResult) ObjectSummary {
	createdAt, createdAtEpoch := clampRFC3339(r.CreatedAt)
	updatedAt, updatedAtEpoch := clampRFC3339(r.UpdatedAt)
	return ObjectSummary{
		ObjectID:       r.ObjectID,
		ObjectType:     r.ObjectType,
		Author:         Author{Name: r.Author.Name, Email: r.Author.Email},
		CreatedAt:      createdAt,
		CreatedAtEpoch: createdAtEpoch,
		UpdatedAt:      updatedAt,
		UpdatedAtEpoch: updatedAtEpoch,
		OpCount:        r.OpCount,
		Verification:   r.Verification,
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

// InitSkip explains why one remote did not get a writ fetch refspec:
// either Init discovered it and could not configure it (an InitRemote
// with Status "skipped"), or Init's one hard failure for the run landed
// on it (Status "failed"). Code is a closed catalogue, deliberately
// narrower than Failure's sync-shaped one -- it exists only to make the
// two things writ.Init's remote gate can produce (writ.ErrUnknownRemote,
// writ.ErrInvalidRemoteName -- see engine/init.go, engine/errors.go)
// distinguishable without parsing English, which is the whole point of
// this ticket (WRIT-304): a url-less remote section never had anything
// configured for it ("unknown-remote"), while a real, named remote writ
// genuinely cannot use ("invalid-name") is a different fact for a script
// to act on. "other" is the honest escape hatch for anything else Ensure
// can fail with, keeping the catalogue closed rather than speculative.
type InitSkip struct {
	Code    string `json:"code"` // unknown-remote | invalid-name | other
	Message string `json:"message"`
}

// InitRemote reports one remote's outcome, matching writ.RemoteInit's four
// meaningful states one-for-one (see that type's godoc in engine/init.go):
//
//   - "configured" / "already-configured" -- Refspec is set; the two
//     statuses distinguish writ.RemoteInit.Repaired (true/false).
//   - "skipped" -- Init discovered this remote (no explicit remote list)
//     and could not configure it; Reason names why.
//   - "not-attempted" -- Init never even tried this remote, because an
//     earlier remote's hard failure stopped the run first. Reason is nil:
//     there is nothing wrong with this remote itself, Init simply never
//     reached it (writ.RemoteInit.NotAttempted).
//   - "failed" -- the one hard failure that stopped the run. Reason names
//     the error.
type InitRemote struct {
	Remote  string    `json:"remote"`
	Status  string    `json:"status"` // configured | already-configured | skipped | failed | not-attempted
	Refspec string    `json:"refspec,omitempty"`
	Reason  *InitSkip `json:"reason,omitempty"`
}

// InitStarterSchema reports the starter writ.schema outcome, mirroring
// writ.InitResult's own StarterSchema* fields.
type InitStarterSchema struct {
	Path      string `json:"path"`
	Namespace string `json:"namespace,omitempty"`
	Written   bool   `json:"written"`
	Existed   bool   `json:"existed"`
	Error     string `json:"error,omitempty"`
}

// InitResult is the `init.result` JSON payload: the one-time repository
// setup `writ init` performs, reported the way its porcelain output
// already does -- every field here traces to a line runInit prints today
// (docs/cli-json.md's `writ init --json` section has the table).
//
// Deliberately absent: the identity/person warnings and their remediation
// `git config` lines. docs/cli-json.md rule 4 puts diagnostics on stderr
// as plain text, and the machine-relevant fact is already carried by
// presence/absence -- no SigningKey means no usable signing key, the same
// "presence is itself the signal" pattern ObjectSummary.CreatedAtEpoch
// already uses. There is no Warnings field for the same reason: it would
// be a second, prose-shaped channel for something the fields already say.
//
// Remotes is always non-nil so it serializes as `[]`, never `null`.
type InitResult struct {
	Outcome           string             `json:"outcome"` // complete | partial | stopped
	WriterID          string             `json:"writer_id"`
	WriterIDMinted    bool               `json:"writer_id_minted"`
	RepoID            string             `json:"repo_id"`
	RepoIDMinted      bool               `json:"repo_id_minted"`
	PersonID          string             `json:"person_id,omitempty"`
	PersonIDSource    string             `json:"person_id_source,omitempty"` // writ.personId | user.email
	SigningKey        string             `json:"signing_key,omitempty"`
	SigningKeyLiteral bool               `json:"signing_key_literal,omitempty"`
	Remotes           []InitRemote       `json:"remotes"`
	StarterSchema     *InitStarterSchema `json:"starter_schema,omitempty"`
}

// classifyInitSkip maps one RemoteInit.Err to the closed InitSkip
// catalogue: "unknown-remote" for writ.ErrUnknownRemote (a url-less
// remote section -- nothing was ever configured for it), "invalid-name"
// for writ.ErrInvalidRemoteName (a real remote writ cannot name), "other"
// for anything else Ensure can fail with (including the one hard failure
// that stops the run). err is assumed non-nil -- callers only reach this
// for a skipped or failed RemoteInit, both of which always carry one.
func classifyInitSkip(err error) *InitSkip {
	code := "other"
	switch {
	case errors.Is(err, writ.ErrUnknownRemote):
		code = "unknown-remote"
	case errors.Is(err, writ.ErrInvalidRemoteName):
		code = "invalid-name"
	}
	return &InitSkip{Code: code, Message: err.Error()}
}

// FromInitResult converts a writ.InitResult and the error writ.Init
// returned (nil on success) into the init.result wire payload. namespace
// is the InitOptions.StarterNamespace the caller passed to the writ.Init
// call that produced res (from --namespace, or the interactive prompt) --
// writ.InitResult itself does not carry it back, the same way runInit's
// own renderStarterSchemaOutcome takes it as a separate parameter.
// FromInitResult derives Outcome so no caller re-derives it:
//
//   - err == nil, no remote "skipped"  -> "complete"
//   - err == nil, >=1 remote "skipped" -> "partial"
//   - err != nil                       -> "stopped"
//
// A starter-schema write failure (res.StarterSchemaErr) never changes
// Outcome -- "partial" means "a remote was skipped", not "anything went
// slightly wrong" -- it is reported only in StarterSchema.Error.
func FromInitResult(res writ.InitResult, err error, namespace string) InitResult {
	out := InitResult{
		WriterID:          res.WriterID,
		WriterIDMinted:    res.WriterIDMinted,
		RepoID:            res.RepoID,
		RepoIDMinted:      res.RepoIDMinted,
		SigningKey:        res.SigningKey,
		SigningKeyLiteral: res.SigningKeyLiteral,
		Remotes:           []InitRemote{},
	}

	if res.PersonIDErr == nil && res.PersonID != "" {
		out.PersonID = res.PersonID
		if res.PersonIDFromKey {
			out.PersonIDSource = "writ.personId"
		} else {
			out.PersonIDSource = "user.email"
		}
	}
	skipped := false
	for _, r := range res.Remotes {
		remote := InitRemote{Remote: r.Name}
		switch {
		case r.NotAttempted:
			remote.Status = "not-attempted"
		case r.Err == nil:
			remote.Refspec = r.Refspec
			if r.Repaired {
				remote.Status = "configured"
			} else {
				remote.Status = "already-configured"
			}
		case r.Skipped:
			remote.Status = "skipped"
			remote.Reason = classifyInitSkip(r.Err)
			skipped = true
		default:
			remote.Status = "failed"
			remote.Reason = classifyInitSkip(r.Err)
		}
		out.Remotes = append(out.Remotes, remote)
	}

	if res.StarterSchemaPath != "" {
		s := &InitStarterSchema{
			Path:    res.StarterSchemaPath,
			Written: res.StarterSchemaWritten,
			Existed: res.StarterSchemaExisted,
		}
		if res.StarterSchemaWritten {
			s.Namespace = namespace
		}
		if res.StarterSchemaErr != nil {
			s.Error = res.StarterSchemaErr.Error()
		}
		out.StarterSchema = s
	}

	switch {
	case err != nil:
		out.Outcome = "stopped"
	case skipped:
		out.Outcome = "partial"
	default:
		out.Outcome = "complete"
	}

	return out
}
