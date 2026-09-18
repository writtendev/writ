package projection

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/state"
)

// ObjectChange describes the modifications made to a collaborative object in an incremental refresh batch.
type ObjectChange struct {
	// ObjectID is the unique identifier of the collaborative object.
	ObjectID string `json:"object_id"`

	// ObjectType is the type of collaborative object, whatever the installed
	// schema declares it to be (e.g. "widget").
	ObjectType string `json:"object_type"`

	// OpTypes lists the distinct operation types applied to the object in this refresh batch.
	OpTypes []string `json:"op_types"`

	// Created reports whether the batch contained the creation operation for this object.
	Created bool `json:"created"`
}

// Stats reports the work performed during a Refresh or Rebuild pass.
type Stats struct {
	// OpsDecoded is the number of commits decoded from git during this pass.
	OpsDecoded int `json:"ops_decoded"`

	// ObjectsTouched is the number of collaborative objects refolded during this pass.
	ObjectsTouched int `json:"objects_touched"`

	// AnchorsResolved is the number of anchor resolution evaluations performed during this pass.
	AnchorsResolved int `json:"anchors_resolved"`

	// Rebuilt reports whether a full from-scratch rebuild was executed (e.g. on rollback, ref deletion, or explicit rebuild).
	Rebuilt bool `json:"rebuilt"`

	// Changed lists the objects modified during an incremental refresh pass.
	// Left empty on a full rebuild, where Rebuilt: true indicates all objects may have changed.
	Changed []ObjectChange `json:"changed,omitempty"`

	// Rejections records op commits that failed reader validation during
	// this pass (dag.EnumerateSince's own Rejections, carried through
	// unchanged). The list covers only the commits this pass walked: an
	// incremental Refresh sees only the chains it enumerated since the
	// stored cursors, so a rejection a chain's tip already passed on a
	// prior pass is not re-reported here, and the cursor still advances
	// past a rejected commit regardless (WRIT-271) — the caller of the
	// pass that observed a rejection is the only caller that sees it,
	// until a full Rebuild re-derives it from a cold walk.
	Rejections []dag.Rejection `json:"rejections,omitempty"`
}

type refreshConfig struct {
	targetRefs         []string
	enumOverride       *dag.EnumerateResult
	rules              map[string][]state.Rule
	trustStore         codec.TrustStore
	trustStoreDigest   string
	trustStoreOverride bool
}

// Option configures a Refresh or Rebuild pass.
type Option func(*refreshConfig)

// WithTargetRefs specifies explicit code ref names to resolve anchors against.
// If omitted, Refresh defaults to resolving against HEAD's ref.
func WithTargetRefs(refs ...string) Option {
	return func(c *refreshConfig) {
		c.targetRefs = append(c.targetRefs, refs...)
	}
}

// WithSchema supplies the rule index (RulesFromSchemas' shape: the built-in
// vocabulary overlaid by whatever the log declares, log wins per type) a
// Refresh or Rebuild pass applies before folding anything. The projection
// cannot resolve schemas itself — package writ resolves once and passes the
// result in, here and in Store.ApplySchema. Omitting it (or passing nil) is
// only for a caller that has already applied a schema on this *DB directly
// and wants this pass to keep using it.
func WithSchema(rules map[string][]state.Rule) Option {
	return func(c *refreshConfig) {
		c.rules = rules
	}
}

// WithLiveTrustStore supplies the trust store to verify decoded commits
// against and the fingerprint of its allowed_signers file's current
// contents, both read fresh by the caller immediately before this call —
// not the ones projection.Open or dag.Open froze at construction time
// (WRIT-251 round 2 finding: freezing either let two long-lived handles
// disagree about the file forever, fighting over one shared projection
// cache, and let a long-lived handle never pick up an edit). ts flows
// into this pass's EnumerateSince call (dag.WithLiveTrustStore); digest
// flows into this pass's ApplySchema call, so an edited allowed_signers
// trips needs_rebuild exactly as a real schema change does (ruling 4). A
// nil ts and empty digest are the same as never calling this at all —
// "no trust store configured or readable".
func WithLiveTrustStore(ts codec.TrustStore, digest string) Option {
	return func(c *refreshConfig) {
		c.trustStore = ts
		c.trustStoreDigest = digest
		c.trustStoreOverride = true
	}
}

// enumerateOptions translates this pass's trust-store override, if any,
// into the dag.EnumerateOption EnumerateSince needs to verify against it
// instead of the Store's own Open-time trust store. Shared by both the
// incremental path and rebuildWithConfig, unlike incrementalSeenOption
// below: a trust-store override applies equally to a cold walk and an
// incremental one, but dag.WithSeen must not (see that method's comment
// and the one at rebuildWithConfig's EnumerateSince call).
func (c *refreshConfig) enumerateOptions() []dag.EnumerateOption {
	if !c.trustStoreOverride {
		return nil
	}
	return []dag.EnumerateOption{dag.WithLiveTrustStore(c.trustStore)}
}

// incrementalSeenOption prepares a dag.WithSeen option backed by the ops
// table's op_id primary key, for Refresh's incremental path only (WRIT-273).
// A commit already recorded there had its full ancestry walked by whichever
// pass first inserted it — see dag.WithSeen's doc comment for why that
// invariant holds for this projection specifically — so EnumerateSince can
// treat it as an already-seen stop point instead of re-decoding its whole
// ancestry every time a new op's causal parent happens to sit deep inside
// another chain. The prepared statement is a point lookup on the primary
// key, reused across every candidate commit in one pass; the caller must
// call the returned close func once this pass's EnumerateSince call
// returns.
func (d *DB) incrementalSeenOption() (dag.EnumerateOption, func(), error) {
	stmt, err := d.db.Prepare("SELECT 1 FROM ops WHERE op_id = ?")
	if err != nil {
		return nil, nil, fmt.Errorf("projection: prepare seen lookup: %w", err)
	}
	seen := func(opID string) bool {
		var one int
		return stmt.QueryRow(opID).Scan(&one) == nil
	}
	return dag.WithSeen(seen), func() { _ = stmt.Close() }, nil
}

// Refresh incrementally brings the projection SQLite cache up to date with the underlying DAG store.
// If a chain rollback or deleted chain ref is detected, Refresh falls through to a full rebuild.
func (d *DB) Refresh(store *dag.Store, opts ...Option) (Stats, error) {
	if d == nil || d.db == nil {
		return Stats{}, fmt.Errorf("projection: database is closed")
	}
	if store == nil {
		return Stats{}, fmt.Errorf("projection: nil dag.Store")
	}

	cfg := &refreshConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	// Fold in this pass's freshly-read trust-store digest (WithLiveTrustStore)
	// before ApplySchema compares it, instead of whatever projection.Open
	// froze at construction (WRIT-251 round 2 finding).
	if cfg.trustStoreOverride {
		d.trustStoreDigest = cfg.trustStoreDigest
	}
	if cfg.rules != nil {
		if err := d.ApplySchema(cfg.rules); err != nil {
			return Stats{}, fmt.Errorf("projection: apply schema: %w", err)
		}
	}
	if err := d.requireMaterializationPlan(); err != nil {
		return Stats{}, err
	}

	targetTips, err := resolveTargetTips(store.Storer(), cfg.targetRefs)
	if err != nil {
		return Stats{}, fmt.Errorf("projection: resolve target tips: %w", err)
	}

	// A schema change (ApplySchema above, or one applied by another process
	// sharing this file) dropped and recreated the generated tables: nothing
	// in them is derivable from an incremental delta on top of a schema that
	// no longer exists, so this pass takes the full-rebuild path exactly as
	// a rewound tip does — the droppable-cache answer to a schema change is
	// drop and rebuild, never migrate.
	if needsRebuild, err := loadMetaBool(d.db, "needs_rebuild"); err != nil {
		return Stats{}, err
	} else if needsRebuild {
		return d.rebuildWithConfig(store, cfg, targetTips)
	}

	// 1. Read stored chain tips
	storedCursors, err := d.loadChainTips()
	if err != nil {
		return Stats{}, fmt.Errorf("projection: load chain tips: %w", err)
	}

	// 2. Discover current chains
	currentChains, err := dag.Chains(store.Storer())
	if err != nil {
		return Stats{}, fmt.Errorf("projection: discover chains: %w", err)
	}

	// Check if any previously stored chain has disappeared
	disappeared := false
	for refName := range storedCursors {
		if _, ok := currentChains[refName]; !ok {
			disappeared = true
			break
		}
	}

	if disappeared {
		// Fall through to full rebuild
		return d.rebuildWithConfig(store, cfg, targetTips)
	}

	// 3. Enumerate delta since stored cursors. dag.WithSeen(...) is scoped
	// to this incremental call alone (see incrementalSeenOption and the
	// comment at rebuildWithConfig's own EnumerateSince call, which must
	// never receive it): it lets the walk stop at any commit already
	// projected instead of re-decoding that commit's whole ancestry, which
	// is what makes an op whose causal parent sits deep inside another
	// chain cost O(new ops) instead of O(history) (WRIT-273).
	var enumRes *dag.EnumerateResult
	if cfg.enumOverride != nil {
		enumRes = cfg.enumOverride
	} else {
		seenOpt, closeSeen, err := d.incrementalSeenOption()
		if err != nil {
			return Stats{}, err
		}
		res, err := store.EnumerateSince(storedCursors, append(cfg.enumerateOptions(), seenOpt)...)
		closeSeen()
		if err != nil {
			return Stats{}, fmt.Errorf("projection: enumerate since cursors: %w", err)
		}
		enumRes = res
	}

	if len(enumRes.Rewound) > 0 {
		// Rollback detected: fall through to full rebuild
		return d.rebuildWithConfig(store, cfg, targetTips)
	}

	// 4. Fast-forward incremental path in a single transaction
	tx, err := d.db.Begin()
	if err != nil {
		return Stats{}, fmt.Errorf("projection: begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// Update chain_tips
	if _, err := tx.Exec("DELETE FROM chain_tips"); err != nil {
		return Stats{}, fmt.Errorf("projection: clear chain_tips: %w", err)
	}
	for refName, tip := range enumRes.Cursors {
		if _, err := tx.Exec("INSERT INTO chain_tips (ref_name, tip) VALUES (?, ?)", refName, tip); err != nil {
			return Stats{}, fmt.Errorf("projection: insert chain tip %s: %w", refName, err)
		}
	}

	// Update code_tips
	if _, err := tx.Exec("DELETE FROM code_tips"); err != nil {
		return Stats{}, fmt.Errorf("projection: clear code_tips: %w", err)
	}
	for refName, tip := range targetTips {
		if _, err := tx.Exec("INSERT INTO code_tips (ref_name, tip) VALUES (?, ?)", refName, tip); err != nil {
			return Stats{}, fmt.Errorf("projection: insert code tip %s: %w", refName, err)
		}
	}

	// Insert newly enumerated ops into ops table
	for _, ops := range enumRes.Ops {
		for _, op := range ops {
			if err := insertOp(tx, op); err != nil {
				return Stats{}, err
			}
		}
	}

	// Refold only touched objects
	desc := d.descriptor()
	for objID := range enumRes.Ops {
		opsForObj, err := readOpsForObject(tx, objID)
		if err != nil {
			return Stats{}, fmt.Errorf("projection: read ops for object %s: %w", objID, err)
		}
		if err := materializeObject(tx, desc, objID, opsForObj); err != nil {
			return Stats{}, fmt.Errorf("projection: materialize object %s: %w", objID, err)
		}
	}

	// Materialize / re-resolve anchors against current code_tips
	anchorsResolved, err := materializeAnchors(tx, desc, store.Storer())
	if err != nil {
		return Stats{}, fmt.Errorf("projection: materialize anchors: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Stats{}, fmt.Errorf("projection: commit refresh: %w", err)
	}

	var changed []ObjectChange
	objIDs := make([]string, 0, len(enumRes.Ops))
	for objID := range enumRes.Ops {
		objIDs = append(objIDs, objID)
	}
	sort.Strings(objIDs)

	for _, objID := range objIDs {
		ops := enumRes.Ops[objID]
		if len(ops) == 0 {
			continue
		}
		objType := state.DetermineObjectType(ops)
		if objType == "" {
			var dbType string
			err := d.db.QueryRow("SELECT object_type FROM objects WHERE object_id = ?", objID).Scan(&dbType)
			if err == nil {
				objType = dbType
			} else if !errors.Is(err, sql.ErrNoRows) {
				return Stats{}, fmt.Errorf("projection: query object_type for %s: %w", objID, err)
			}
		}
		var opTypes []string
		seenOpTypes := make(map[string]bool)
		created := false
		for _, op := range ops {
			if !seenOpTypes[op.OpType] {
				seenOpTypes[op.OpType] = true
				opTypes = append(opTypes, op.OpType)
			}
			if op.OpType == "create" {
				created = true
			}
		}
		sort.Strings(opTypes)
		changed = append(changed, ObjectChange{
			ObjectID:   objID,
			ObjectType: objType,
			OpTypes:    opTypes,
			Created:    created,
		})
	}

	return Stats{
		OpsDecoded:      enumRes.DecodedCommits,
		ObjectsTouched:  len(enumRes.Ops),
		AnchorsResolved: anchorsResolved,
		Rebuilt:         false,
		Changed:         changed,
		Rejections:      enumRes.Rejections,
	}, nil
}

// Rebuild completely discards and recreates the projection cache from a cold walk of all writ chains.
func (d *DB) Rebuild(store *dag.Store, opts ...Option) (Stats, error) {
	if d == nil || d.db == nil {
		return Stats{}, fmt.Errorf("projection: database is closed")
	}
	if store == nil {
		return Stats{}, fmt.Errorf("projection: nil dag.Store")
	}

	cfg := &refreshConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	// See the matching comment in Refresh: fold in the freshly-read digest
	// before ApplySchema compares it.
	if cfg.trustStoreOverride {
		d.trustStoreDigest = cfg.trustStoreDigest
	}
	if cfg.rules != nil {
		if err := d.ApplySchema(cfg.rules); err != nil {
			return Stats{}, fmt.Errorf("projection: apply schema: %w", err)
		}
	}
	if err := d.requireMaterializationPlan(); err != nil {
		return Stats{}, err
	}

	targetTips, err := resolveTargetTips(store.Storer(), cfg.targetRefs)
	if err != nil {
		return Stats{}, fmt.Errorf("projection: resolve target tips: %w", err)
	}

	return d.rebuildWithConfig(store, cfg, targetTips)
}

func (d *DB) rebuildWithConfig(store *dag.Store, cfg *refreshConfig, targetTips map[string]string) (Stats, error) {
	// Deliberately no dag.WithSeen here, unlike the incremental call in
	// Refresh above: this runs before the tables below are truncated, so a
	// rebuild has to be a genuine cold walk of every commit or the
	// droppable-cache guarantee (AGENTS.md: "the SQLite projection is a
	// droppable cache, never a source of truth") breaks — a caller who
	// drops and rebuilds this projection must get back exactly what a cold
	// walk produces, not whatever the pre-rebuild ops table happened to
	// already contain. Do not "tidy" this to share incrementalSeenOption
	// with the call above.
	enumRes, err := store.EnumerateSince(nil, cfg.enumerateOptions()...)
	if err != nil {
		return Stats{}, fmt.Errorf("projection: cold enumerate: %w", err)
	}

	desc := d.descriptor()

	tx, err := d.db.Begin()
	if err != nil {
		return Stats{}, fmt.Errorf("projection: begin rebuild transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// Clear every substrate table except meta (which carries schema_version,
	// schema_digest, schema_tables, schema_descriptor and schema_query_shapes,
	// none of which a data rebuild should touch) and every generated table
	// the current descriptor knows about.
	for _, t := range substrateTables {
		if t == "meta" {
			continue
		}
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return Stats{}, fmt.Errorf("projection: truncate table %s: %w", t, err)
		}
	}
	for _, t := range desc.allTables() {
		if _, err := tx.Exec("DELETE FROM " + quoteIdent(t.Name)); err != nil {
			return Stats{}, fmt.Errorf("projection: truncate table %s: %w", t.Name, err)
		}
	}

	// Insert chain_tips
	for refName, tip := range enumRes.Cursors {
		if _, err := tx.Exec("INSERT INTO chain_tips (ref_name, tip) VALUES (?, ?)", refName, tip); err != nil {
			return Stats{}, fmt.Errorf("projection: insert chain tip %s: %w", refName, err)
		}
	}

	// Insert code_tips
	for refName, tip := range targetTips {
		if _, err := tx.Exec("INSERT INTO code_tips (ref_name, tip) VALUES (?, ?)", refName, tip); err != nil {
			return Stats{}, fmt.Errorf("projection: insert code tip %s: %w", refName, err)
		}
	}

	// Insert all ops
	for _, ops := range enumRes.Ops {
		for _, op := range ops {
			if err := insertOp(tx, op); err != nil {
				return Stats{}, err
			}
		}
	}

	// Fold all objects
	for objID := range enumRes.Ops {
		opsForObj, err := readOpsForObject(tx, objID)
		if err != nil {
			return Stats{}, fmt.Errorf("projection: read ops for object %s: %w", objID, err)
		}
		if err := materializeObject(tx, desc, objID, opsForObj); err != nil {
			return Stats{}, fmt.Errorf("projection: materialize object %s: %w", objID, err)
		}
	}

	// Materialize anchors
	anchorsResolved, err := materializeAnchors(tx, desc, store.Storer())
	if err != nil {
		return Stats{}, fmt.Errorf("projection: materialize anchors: %w", err)
	}

	if _, err := tx.Exec("DELETE FROM meta WHERE key = 'needs_rebuild'"); err != nil {
		return Stats{}, fmt.Errorf("projection: clear needs_rebuild: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Stats{}, fmt.Errorf("projection: commit rebuild: %w", err)
	}

	return Stats{
		OpsDecoded:      enumRes.DecodedCommits,
		ObjectsTouched:  len(enumRes.Ops),
		AnchorsResolved: anchorsResolved,
		Rebuilt:         true,
		Rejections:      enumRes.Rejections,
	}, nil
}

func (d *DB) loadChainTips() (dag.CursorSet, error) {
	rows, err := d.db.Query("SELECT ref_name, tip FROM chain_tips")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cursors := make(dag.CursorSet)
	for rows.Next() {
		var refName, tip string
		if err := rows.Scan(&refName, &tip); err != nil {
			return nil, err
		}
		cursors[refName] = tip
	}
	return cursors, rows.Err()
}

func insertOp(tx *sql.Tx, op codec.Op) error {
	parentsJSON, err := json.Marshal(op.Parents)
	if err != nil {
		return fmt.Errorf("projection: marshal parents for op %s: %w", op.ID, err)
	}

	payload := op.Raw
	if len(payload) == 0 {
		encoded, err := codec.EncodePayload(op.Envelope)
		if err != nil {
			return fmt.Errorf("projection: encode payload for op %s: %w", op.ID, err)
		}
		payload = encoded
	}

	authorTime := op.Author.When.UTC().Unix()
	authorTZ := op.Author.When.Format("-0700")
	committerTime := op.Committer.When.UTC().Unix()
	committerTZ := op.Committer.When.Format("-0700")

	var sig sql.NullString
	if op.Signature != "" {
		sig = sql.NullString{String: op.Signature, Valid: true}
	}

	var fingerprint sql.NullString
	if op.Verification.KeyFingerprint != "" {
		fingerprint = sql.NullString{String: op.Verification.KeyFingerprint, Valid: true}
	}

	_, err = tx.Exec(`
		INSERT OR REPLACE INTO ops (
			op_id, object_id, object_type, op_type, op_version,
			parents, author_name, author_email, author_time, author_tz,
			committer_name, committer_email, committer_time, committer_tz,
			message, signature, payload, verification, key_fingerprint
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		op.ID, op.ObjectID, op.ObjectType, op.OpType, op.OpVersion,
		string(parentsJSON), op.Author.Name, op.Author.Email, authorTime, authorTZ,
		op.Committer.Name, op.Committer.Email, committerTime, committerTZ,
		op.Message, sig, payload, string(op.Verification.Outcome), fingerprint,
	)
	if err != nil {
		return fmt.Errorf("projection: insert op %s: %w", op.ID, err)
	}
	return nil
}

func readOpsForObject(tx *sql.Tx, objectID string) ([]codec.Op, error) {
	rows, err := tx.Query(`
		SELECT op_id, parents, author_name, author_email, author_time, author_tz,
		       committer_name, committer_email, committer_time, committer_tz,
		       message, signature, payload, verification, key_fingerprint
		FROM ops
		WHERE object_id = ?
		ORDER BY op_id ASC
	`, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ops []codec.Op
	for rows.Next() {
		var (
			opID, parentsJSON, authorName, authorEmail, authorTZ string
			authorTime, committerTime                            int64
			committerName, committerEmail, committerTZ, message  string
			sig, fingerprint                                     sql.NullString
			payload                                              []byte
			verification                                         string
		)

		if err := rows.Scan(
			&opID, &parentsJSON, &authorName, &authorEmail, &authorTime, &authorTZ,
			&committerName, &committerEmail, &committerTime, &committerTZ,
			&message, &sig, &payload, &verification, &fingerprint,
		); err != nil {
			return nil, err
		}

		env, err := codec.DecodePayload(payload)
		if err != nil {
			return nil, fmt.Errorf("projection: decode payload for op %s: %w", opID, err)
		}

		var parents []string
		if len(parentsJSON) > 0 {
			if err := json.Unmarshal([]byte(parentsJSON), &parents); err != nil {
				return nil, fmt.Errorf("projection: unmarshal parents for op %s: %w", opID, err)
			}
		}

		authorWhen := parseTimeWithZone(authorTime, authorTZ)
		committerWhen := parseTimeWithZone(committerTime, committerTZ)

		var signature string
		if sig.Valid {
			signature = sig.String
		}

		var keyFingerprint string
		if fingerprint.Valid {
			keyFingerprint = fingerprint.String
		}

		ops = append(ops, codec.Op{
			Envelope:  env,
			ID:        opID,
			Parents:   parents,
			Author:    codec.Identity{Name: authorName, Email: authorEmail, When: authorWhen},
			Committer: codec.Identity{Name: committerName, Email: committerEmail, When: committerWhen},
			Message:   message,
			Signature: signature,
			Verification: codec.Verification{
				Valid:          codec.VerificationOutcome(verification) == codec.OutcomeValid,
				Outcome:        codec.VerificationOutcome(verification),
				KeyFingerprint: keyFingerprint,
				Principal:      authorEmail,
			},
		})
	}

	return ops, rows.Err()
}

func parseTimeWithZone(sec int64, tz string) time.Time {
	t, err := time.Parse("-0700", tz)
	if err == nil {
		return time.Unix(sec, 0).In(t.Location())
	}
	return time.Unix(sec, 0).UTC()
}

func peelToCommit(s storage.Storer, h plumbing.Hash) plumbing.Hash {
	if s == nil || h.IsZero() {
		return h
	}
	curr := h
	for {
		tag, err := object.GetTag(s, curr)
		if err != nil || tag == nil {
			break
		}
		if tag.TargetType == plumbing.CommitObject {
			return tag.Target
		}
		if tag.TargetType == plumbing.TagObject {
			curr = tag.Target
			continue
		}
		break
	}
	return curr
}

func resolveTargetTips(s storage.Storer, explicitRefs []string) (map[string]string, error) {
	targetTips := make(map[string]string)
	if s == nil {
		return targetTips, nil
	}

	if len(explicitRefs) == 0 {
		// Default to HEAD
		headRef, err := storer.ResolveReference(s, plumbing.HEAD)
		if err == nil && headRef != nil {
			targetTips[headRef.Name().String()] = peelToCommit(s, headRef.Hash()).String()
		}
		return targetTips, nil
	}

	for _, refName := range explicitRefs {
		if refName == "HEAD" {
			headRef, err := storer.ResolveReference(s, plumbing.HEAD)
			if err == nil && headRef != nil {
				targetTips["HEAD"] = peelToCommit(s, headRef.Hash()).String()
			}
			continue
		}

		// Try explicit reference and standard git revision prefix candidates,
		// in the precedence order gitrevisions defines (tags before heads).
		candidates := []plumbing.ReferenceName{
			plumbing.ReferenceName(refName),
			plumbing.ReferenceName("refs/" + refName),
			plumbing.ReferenceName("refs/tags/" + refName),
			plumbing.ReferenceName("refs/heads/" + refName),
			plumbing.ReferenceName("refs/remotes/" + refName),
			plumbing.ReferenceName("refs/remotes/" + refName + "/HEAD"),
		}
		resolved := false
		for _, cand := range candidates {
			ref, err := storer.ResolveReference(s, cand)
			if err == nil && ref != nil {
				targetTips[refName] = peelToCommit(s, ref.Hash()).String()
				resolved = true
				break
			}
		}
		if resolved {
			continue
		}

		// Try looking up by commit hash if ReferenceName lookup fails
		h := plumbing.NewHash(refName)
		if !h.IsZero() {
			if _, err := s.EncodedObject(plumbing.CommitObject, h); err == nil {
				targetTips[refName] = h.String()
			} else {
				peeled := peelToCommit(s, h)
				if peeled != h && !peeled.IsZero() {
					targetTips[refName] = peeled.String()
				}
			}
		}
	}

	return targetTips, nil
}
