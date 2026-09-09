package projection_test

import (
	"github.com/writtendev/writ/engine/state"
)

// testRules is the one definition of "a schema-shaped rule index" every
// projection test shares. Writ hard-codes no object type but `schema`, so
// there is no built-in vocabulary to derive one from any more: the types
// below are declared here the way a consumer declares them in the log, using
// the neutral example types the corpus uses. Hand-built rather than routed
// through schemasrc/RulesFromSchemas: ApplySchema/Refresh take a plain
// map[string][]state.Rule regardless of where it came from, and a literal
// table costs the same to read as a compiled one for a fixed, small schema.
func testRules() map[string][]state.Rule {
	return map[string][]state.Rule{
		"widget": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"},
			{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "text", ObjectType: "widget"},
			{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"},
			{OpType: "update", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "text", ObjectType: "widget"},
			{OpType: "assign", OpVersion: 1, Field: "add", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref", ObjectType: "widget"},
			{OpType: "assign", OpVersion: 1, Field: "remove", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref", ObjectType: "widget"},
			{OpType: "tag", OpVersion: 1, Field: "add", Target: "tags", Strategy: "set-observed-remove", ValueType: "string", ObjectType: "widget"},
			{OpType: "tag", OpVersion: 1, Field: "remove", Target: "tags", Strategy: "set-observed-remove", ValueType: "string", ObjectType: "widget"},
			{OpType: "revision", OpVersion: 1, Field: "base", Strategy: "append", ValueType: "git-oid", ObjectType: "widget"},
			{OpType: "revision", OpVersion: 1, Field: "head", Strategy: "append", ValueType: "git-oid", ObjectType: "widget"},
			{OpType: "endorse", OpVersion: 1, Field: "revision", Strategy: "keyed-lww", Key: []string{"subject", "revision"}, KeyTypes: map[string]string{"subject": "person-ref", "revision": "git-oid"}, ValueType: "git-oid", ObjectType: "widget"},
			{OpType: "endorse", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww", Key: []string{"subject", "revision"}, KeyTypes: map[string]string{"subject": "person-ref", "revision": "git-oid"}, ValueType: "enum", Enum: []string{"yes", "no"}, ObjectType: "widget"},
			{OpType: "check", OpVersion: 1, Field: "revision", Target: "check_revision", Strategy: "keyed-lww", Key: []string{"revision", "name"}, KeyTypes: map[string]string{"revision": "git-oid", "name": "string"}, ValueType: "git-oid", ObjectType: "widget"},
			{OpType: "check", OpVersion: 1, Field: "state", Strategy: "keyed-lww", Key: []string{"revision", "name"}, KeyTypes: map[string]string{"revision": "git-oid", "name": "string"}, ValueType: "enum", Enum: []string{"pending", "passed", "failed"}, ObjectType: "widget"},
			{OpType: "archive", OpVersion: 1, Field: "archived", Strategy: "tombstone", ValueType: "bool", ObjectType: "widget"},
		},
		"gadget": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "gadget"},
			{OpType: "create", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position", ObjectType: "gadget"},
			{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "gadget"},
			{OpType: "assign", OpVersion: 1, Field: "add", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref", ObjectType: "gadget"},
			{OpType: "assign", OpVersion: 1, Field: "remove", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref", ObjectType: "gadget"},
			{OpType: "tag", OpVersion: 1, Field: "add", Target: "tags", Strategy: "set-observed-remove", ValueType: "string", ObjectType: "gadget"},
			{OpType: "tag", OpVersion: 1, Field: "remove", Target: "tags", Strategy: "set-observed-remove", ValueType: "string", ObjectType: "gadget"},
		},
	}
}

// neutralTestRules declares one schema-shaped type, "ticket", for tests
// exercising the generic Objects/Object query path that have no reason to
// depend on the fuller table above.
func neutralTestRules() map[string][]state.Rule {
	return map[string][]state.Rule{
		"ticket": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
			{OpType: "create", OpVersion: 1, Field: "status", Strategy: "lww", ValueType: "string"},
			{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
			{OpType: "update", OpVersion: 1, Field: "status", Strategy: "lww", ValueType: "string"},
			{OpType: "assign", OpVersion: 1, Field: "add", Target: "assignees", Strategy: "set-observed-remove", ValueType: "string"},
			{OpType: "assign", OpVersion: 1, Field: "remove", Target: "assignees", Strategy: "set-observed-remove", ValueType: "string"},
		},
	}
}
