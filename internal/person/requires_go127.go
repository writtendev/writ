//go:build !go1.27

package person

// This file exists to fail the build, on purpose, under any toolchain older
// than Go 1.27: the reference to an undefined identifier below is the error,
// and its name is the message. It costs nothing at run time and is never
// compiled by a toolchain that satisfies go.mod.
//
// WRIT-315 (and WRIT-241 before it) ruled Go 1.27 the minimum. go.mod's go
// directive says so too, but a directive is advisory for a build that runs
// with GOTOOLCHAIN=local on an older distro, Nix or hermetic Go, so this is
// the check that names the reason there.
//
// What it guards is narrower than it once was. The person-identifier fold no
// longer reads Unicode data from the toolchain: internal/person/ucd vendors
// the 17.0.0 tables, so the folded state of a log is the same whoever built
// the reader, and this file is not what keeps it so. It keeps the tests that
// cross-check those tables against golang.org/x/text and the standard
// library meaningful, since those carry Unicode 17.0.0 only from Go 1.27. It
// does not need revisiting at every Go release.
var _ = writ_requires_go1_27_see_WRIT_315_and_spec_identifiers_md_unicode_pin
