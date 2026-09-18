package writ

import (
	"context"
	"fmt"
	"time"

	"github.com/writtendev/writ/internal/projection"
)

// ReadState provides operations for tracking read/unread state of collaborative objects.
type ReadState struct {
	store *Store
}

// Mark records the specified object as read up to the current time.
func (r *ReadState) Mark(ctx context.Context, objectID string) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("writ: store is nil")
	}
	if objectID == "" {
		return fmt.Errorf("writ: object id cannot be empty")
	}

	var lastOpID string
	obj, err := r.store.projection.Object(objectID)
	if err == nil {
		lastOpID = obj.LastOpID
	}

	now := time.Now().UTC()
	if err := r.store.projection.MarkRead(objectID, lastOpID, now); err != nil {
		return fmt.Errorf("writ: mark read: %w", err)
	}
	return nil
}

// Clear removes the read mark for the specified object.
func (r *ReadState) Clear(ctx context.Context, objectID string) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("writ: store is nil")
	}
	if objectID == "" {
		return fmt.Errorf("writ: object id cannot be empty")
	}

	if err := r.store.projection.ClearRead(objectID); err != nil {
		return fmt.Errorf("writ: clear read: %w", err)
	}
	return nil
}

// Unread evaluates which of the given object IDs (or all objects in the projection if ids is empty)
// have never been read or have been updated since they were last read.
func (r *ReadState) Unread(ctx context.Context, ids ...string) ([]string, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}

	type objInfo struct {
		id        string
		updatedAt time.Time
	}
	var objects []objInfo

	if len(ids) == 0 {
		objs, err := r.store.projection.Objects(projection.ObjectFilter{})
		if err != nil {
			return nil, fmt.Errorf("writ: query objects for unread: %w", err)
		}
		objects = make([]objInfo, len(objs))
		for i, o := range objs {
			objects[i] = objInfo{id: o.ObjectID, updatedAt: o.UpdatedAt}
		}
	} else {
		for _, id := range ids {
			obj, err := r.store.projection.Object(id)
			if err != nil {
				if err == ErrNotFound {
					continue
				}
				return nil, fmt.Errorf("writ: query object %s for unread: %w", id, err)
			}
			objects = append(objects, objInfo{id: obj.ObjectID, updatedAt: obj.UpdatedAt})
		}
	}

	if len(objects) == 0 {
		return nil, nil
	}

	objIDs := make([]string, len(objects))
	for i, o := range objects {
		objIDs[i] = o.id
	}

	marks, err := r.store.projection.ReadMarks(objIDs...)
	if err != nil {
		return nil, fmt.Errorf("writ: fetch read marks: %w", err)
	}

	var unread []string
	for _, o := range objects {
		mark, found := marks[o.id]
		if !found {
			unread = append(unread, o.id)
			continue
		}
		if o.updatedAt.After(mark.LastReadAt) {
			unread = append(unread, o.id)
		}
	}

	return unread, nil
}
