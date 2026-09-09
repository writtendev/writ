package projection

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/writtendev/writ/engine/resolve"
)

// Draft represents an unpublished local draft.
type Draft struct {
	DraftID     string          `json:"draft_id"`
	SubjectType string          `json:"subject_type"`
	SubjectID   string          `json:"subject_id"`
	InReplyTo   string          `json:"in_reply_to,omitempty"`
	Anchor      *resolve.Anchor `json:"anchor,omitempty"`
	Text        string          `json:"text"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// DraftFilter specifies filtering criteria when querying drafts.
type DraftFilter struct {
	SubjectID   string `json:"subject_id,omitempty"`
	SubjectType string `json:"subject_type,omitempty"`
}

// ReadMark represents a local read mark for an object.
type ReadMark struct {
	ObjectID     string    `json:"object_id"`
	LastReadAt   time.Time `json:"last_read_at"`
	LastReadOpID string    `json:"last_read_op_id,omitempty"`
}

// SyncCursor represents the recorded tip and timestamp of a synced remote chain ref.
type SyncCursor struct {
	Remote       string    `json:"remote"`
	RefName      string    `json:"ref_name"`
	Tip          string    `json:"tip"`
	LastSyncedAt time.Time `json:"last_synced_at"`
}

func mintDraftID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("projection: rand.Read failed: %v", err))
	}
	return "draft-" + hex.EncodeToString(b)
}

// LocalDB returns the underlying *sql.DB connection pool for the local database.
func (d *DB) LocalDB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.localDB
}

// SaveDraft inserts or updates a draft. If draft.DraftID is empty, a unique draft ID is minted.
func (d *DB) SaveDraft(draft Draft) (string, error) {
	if d == nil || d.localDB == nil {
		return "", fmt.Errorf("projection: local database is closed")
	}

	if draft.DraftID == "" {
		draft.DraftID = mintDraftID()
	}

	now := time.Now().UTC()
	if draft.CreatedAt.IsZero() {
		draft.CreatedAt = now
	}
	draft.UpdatedAt = now

	var anchorStr string
	if draft.Anchor != nil {
		b, err := json.Marshal(draft.Anchor)
		if err != nil {
			return "", fmt.Errorf("projection: marshal draft anchor: %w", err)
		}
		anchorStr = string(b)
	}

	_, err := d.localDB.Exec(`
		INSERT OR REPLACE INTO drafts (
			draft_id, subject_type, subject_id, in_reply_to, anchor, text, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		draft.DraftID,
		draft.SubjectType,
		draft.SubjectID,
		draft.InReplyTo,
		anchorStr,
		draft.Text,
		draft.CreatedAt.Unix(),
		draft.UpdatedAt.Unix(),
	)
	if err != nil {
		return "", fmt.Errorf("projection: insert draft: %w", err)
	}

	return draft.DraftID, nil
}

// Draft retrieves a single draft by its draft ID.
func (d *DB) Draft(draftID string) (Draft, error) {
	if d == nil || d.localDB == nil {
		return Draft{}, fmt.Errorf("projection: local database is closed")
	}

	var (
		dr                                                   Draft
		anchorStr                                            string
		createdAtSec, updatedAtSec                           int64
	)

	err := d.localDB.QueryRow(`
		SELECT draft_id, subject_type, subject_id, in_reply_to, anchor, text, created_at, updated_at
		FROM drafts
		WHERE draft_id = ?
	`, draftID).Scan(
		&dr.DraftID,
		&dr.SubjectType,
		&dr.SubjectID,
		&dr.InReplyTo,
		&anchorStr,
		&dr.Text,
		&createdAtSec,
		&updatedAtSec,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return Draft{}, ErrNotFound
		}
		return Draft{}, fmt.Errorf("projection: query draft %s: %w", draftID, err)
	}

	dr.CreatedAt = time.Unix(createdAtSec, 0).UTC()
	dr.UpdatedAt = time.Unix(updatedAtSec, 0).UTC()

	if len(anchorStr) > 0 {
		anc, err := resolve.ParseAnchor([]byte(anchorStr))
		if err != nil {
			return Draft{}, fmt.Errorf("projection: parse draft anchor %s: %w", draftID, err)
		}
		dr.Anchor = &anc
	}

	return dr, nil
}

// ListDrafts queries drafts matching the provided filter.
func (d *DB) ListDrafts(filter DraftFilter) ([]Draft, error) {
	if d == nil || d.localDB == nil {
		return nil, fmt.Errorf("projection: local database is closed")
	}

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT draft_id, subject_type, subject_id, in_reply_to, anchor, text, created_at, updated_at FROM drafts WHERE 1=1")

	if filter.SubjectID != "" {
		sb.WriteString(" AND subject_id = ?")
		args = append(args, filter.SubjectID)
	}
	if filter.SubjectType != "" {
		sb.WriteString(" AND subject_type = ?")
		args = append(args, filter.SubjectType)
	}

	sb.WriteString(" ORDER BY created_at ASC, draft_id ASC")

	rows, err := d.localDB.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query drafts: %w", err)
	}
	defer rows.Close()

	var drafts []Draft
	for rows.Next() {
		var (
			dr                         Draft
			anchorStr                  string
			createdAtSec, updatedAtSec int64
		)

		if err := rows.Scan(
			&dr.DraftID,
			&dr.SubjectType,
			&dr.SubjectID,
			&dr.InReplyTo,
			&anchorStr,
			&dr.Text,
			&createdAtSec,
			&updatedAtSec,
		); err != nil {
			return nil, fmt.Errorf("projection: scan draft row: %w", err)
		}

		dr.CreatedAt = time.Unix(createdAtSec, 0).UTC()
		dr.UpdatedAt = time.Unix(updatedAtSec, 0).UTC()

		if len(anchorStr) > 0 {
			anc, err := resolve.ParseAnchor([]byte(anchorStr))
			if err != nil {
				return nil, fmt.Errorf("projection: parse draft anchor %s: %w", dr.DraftID, err)
			}
			dr.Anchor = &anc
		}

		drafts = append(drafts, dr)
	}

	return drafts, rows.Err()
}

// DeleteDraft removes a draft by its draft ID.
func (d *DB) DeleteDraft(draftID string) error {
	if d == nil || d.localDB == nil {
		return fmt.Errorf("projection: local database is closed")
	}

	res, err := d.localDB.Exec("DELETE FROM drafts WHERE draft_id = ?", draftID)
	if err != nil {
		return fmt.Errorf("projection: delete draft %s: %w", draftID, err)
	}

	rows, err := res.RowsAffected()
	if err == nil && rows == 0 {
		return ErrNotFound
	}

	return nil
}

// MarkRead marks an object as read with the given timestamp and last-read op ID.
func (d *DB) MarkRead(objectID, lastReadOpID string, readAt time.Time) error {
	if d == nil || d.localDB == nil {
		return fmt.Errorf("projection: local database is closed")
	}

	if readAt.IsZero() {
		readAt = time.Now().UTC()
	}

	_, err := d.localDB.Exec(`
		INSERT OR REPLACE INTO read_state (object_id, last_read_at, last_read_op_id)
		VALUES (?, ?, ?)
	`, objectID, readAt.Unix(), lastReadOpID)
	if err != nil {
		return fmt.Errorf("projection: mark read %s: %w", objectID, err)
	}

	return nil
}

// ClearRead removes the read mark for an object.
func (d *DB) ClearRead(objectID string) error {
	if d == nil || d.localDB == nil {
		return fmt.Errorf("projection: local database is closed")
	}

	_, err := d.localDB.Exec("DELETE FROM read_state WHERE object_id = ?", objectID)
	if err != nil {
		return fmt.Errorf("projection: clear read %s: %w", objectID, err)
	}

	return nil
}

// ReadMarks returns a map of read marks for the given object IDs (or all read marks if no IDs are specified).
func (d *DB) ReadMarks(objectIDs ...string) (map[string]ReadMark, error) {
	if d == nil || d.localDB == nil {
		return nil, fmt.Errorf("projection: local database is closed")
	}

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT object_id, last_read_at, last_read_op_id FROM read_state")
	if len(objectIDs) > 0 {
		sb.WriteString(" WHERE object_id IN (" + placeholders(len(objectIDs)) + ")")
		for _, id := range objectIDs {
			args = append(args, id)
		}
	}

	rows, err := d.localDB.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query read marks: %w", err)
	}
	defer rows.Close()

	marks := make(map[string]ReadMark)
	for rows.Next() {
		var (
			m           ReadMark
			lastReadSec int64
		)
		if err := rows.Scan(&m.ObjectID, &lastReadSec, &m.LastReadOpID); err != nil {
			return nil, fmt.Errorf("projection: scan read mark: %w", err)
		}
		m.LastReadAt = time.Unix(lastReadSec, 0).UTC()
		marks[m.ObjectID] = m
	}

	return marks, rows.Err()
}

// SetSyncCursor records the sync cursor tip and timestamp for a remote and ref name.
func (d *DB) SetSyncCursor(remote, refName, tip string, lastSyncedAt time.Time) error {
	if d == nil || d.localDB == nil {
		return fmt.Errorf("projection: local database is closed")
	}

	if lastSyncedAt.IsZero() {
		lastSyncedAt = time.Now().UTC()
	}

	_, err := d.localDB.Exec(`
		INSERT OR REPLACE INTO sync_cursors (remote, ref_name, tip, last_synced_at)
		VALUES (?, ?, ?, ?)
	`, remote, refName, tip, lastSyncedAt.Unix())
	if err != nil {
		return fmt.Errorf("projection: set sync cursor %s/%s: %w", remote, refName, err)
	}

	return nil
}

// SyncCursors returns the recorded sync cursors for the given remote (or all remotes if remote is empty).
func (d *DB) SyncCursors(remote string) ([]SyncCursor, error) {
	if d == nil || d.localDB == nil {
		return nil, fmt.Errorf("projection: local database is closed")
	}

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT remote, ref_name, tip, last_synced_at FROM sync_cursors")
	if remote != "" {
		sb.WriteString(" WHERE remote = ?")
		args = append(args, remote)
	}
	sb.WriteString(" ORDER BY remote ASC, ref_name ASC")

	rows, err := d.localDB.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query sync cursors: %w", err)
	}
	defer rows.Close()

	var cursors []SyncCursor
	for rows.Next() {
		var (
			sc          SyncCursor
			syncedAtSec int64
		)
		if err := rows.Scan(&sc.Remote, &sc.RefName, &sc.Tip, &syncedAtSec); err != nil {
			return nil, fmt.Errorf("projection: scan sync cursor: %w", err)
		}
		sc.LastSyncedAt = time.Unix(syncedAtSec, 0).UTC()
		cursors = append(cursors, sc)
	}

	return cursors, rows.Err()
}

// DumpLocalTables returns a deterministic dump of all local tables and their rows.
func (d *DB) DumpLocalTables() (map[string][]map[string]any, error) {
	if d == nil || d.localDB == nil {
		return nil, fmt.Errorf("projection: local database is closed")
	}

	dump := make(map[string][]map[string]any)

	for _, table := range localTables {
		query, ok := localTableQueries[table]
		if !ok {
			return nil, fmt.Errorf("missing query for local table %s", table)
		}
		rows, err := d.localDB.Query(query)
		if err != nil {
			return nil, fmt.Errorf("query local table %s: %w", table, err)
		}

		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("get columns for local table %s: %w", table, err)
		}

		var tableRows []map[string]any
		for rows.Next() {
			values := make([]any, len(cols))
			valuePtrs := make([]any, len(cols))
			for i := range values {
				valuePtrs[i] = &values[i]
			}

			if err := rows.Scan(valuePtrs...); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan row in local table %s: %w", table, err)
			}

			rowMap := make(map[string]any, len(cols))
			for i, col := range cols {
				val := values[i]
				if b, ok := val.([]byte); ok {
					rowMap[col] = string(b)
				} else {
					rowMap[col] = val
				}
			}
			tableRows = append(tableRows, rowMap)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate local table %s rows: %w", table, err)
		}

		dump[table] = tableRows
	}

	return dump, nil
}
