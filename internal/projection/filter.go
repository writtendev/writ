package projection

import (
	"strings"
)

// OrderBy specifies the sort order for query results.
type OrderBy string

const (
	OrderByCreatedAtAsc  OrderBy = "created_at_asc"
	OrderByCreatedAtDesc OrderBy = "created_at_desc"
	OrderByUpdatedAtAsc  OrderBy = "updated_at_asc"
	OrderByUpdatedAtDesc OrderBy = "updated_at_desc"
)

// ObjectFilter specifies filter criteria when querying collaborative objects cross-type.
type ObjectFilter struct {
	Type           []string
	Author         []string
	Text           string
	IncludeDeleted bool
	OrderBy        OrderBy
	Limit          int
	Offset         int
}

// escapeLike escapes special SQLite LIKE pattern characters (%, _, \) so that
// the text is matched literally as a substring.
func escapeLike(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' || c == '_' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}
