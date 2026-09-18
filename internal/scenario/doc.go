// Package scenario implements an end-to-end scenario test harness that scripts
// multi-writer workflows (concurrent appends, branch commits, pushes, force-pushes,
// delayed fetches, rollbacks) against throwaway bare git remotes.
//
// All clones drive real transport via system git (engine/sync) and atomic append
// chains via dag.Store, asserting byte-for-byte converged folded state across all devices.
//
// Notes:
//   - Ops are unsigned: codec.DecodeCommit does not verify signatures during enumeration,
//     so signing would add wall-clock and an ssh-keygen dependency without adding coverage;
//     signature verification is tested comprehensively in the envelope fixture family.
//   - Projection convergence is not asserted here: the projection layer is tested independently
//     and the public API (writ.Open) will compose projection + sync in a later ticket.
package scenario
