package dag

// CursorSet maps a chain ref name (e.g. "refs/writ/0123456789abcdef/widget" or
// "refs/remotes/origin/writ/0123456789abcdef/widget") to its last observed tip commit SHA.
type CursorSet map[string]string

// NewCursorSet returns an initialized empty CursorSet.
func NewCursorSet() CursorSet {
	return make(CursorSet)
}

// Clone returns a shallow copy of the CursorSet.
func (cs CursorSet) Clone() CursorSet {
	if cs == nil {
		return nil
	}
	dup := make(CursorSet, len(cs))
	for k, v := range cs {
		dup[k] = v
	}
	return dup
}

// Get returns the tip commit SHA for the specified chain ref name.
func (cs CursorSet) Get(refName string) (string, bool) {
	if cs == nil {
		return "", false
	}
	v, ok := cs[refName]
	return v, ok
}

// Set stores the tip commit SHA for the specified chain ref name.
func (cs CursorSet) Set(refName, tipSHA string) {
	cs[refName] = tipSHA
}
