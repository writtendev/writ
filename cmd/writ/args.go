package main

import (
	"flag"
	"strings"
)

// stringSliceFlag collects a repeatable string flag (e.g. -author a
// -author b) into a slice, in the order given.
type stringSliceFlag []string

func (f *stringSliceFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringSliceFlag) Set(val string) error {
	*f = append(*f, val)
	return nil
}

// parseArgs parses args against fs, interleaving positional arguments and
// flags so that a positional argument may be followed by more flags (for
// example "writ issue status <id> -reason foo"), which flag.FlagSet's own
// Parse does not support on its own: it stops consuming flags at the first
// non-flag argument.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var posArgs []string
	remaining := args
	for len(remaining) > 0 {
		if err := fs.Parse(remaining); err != nil {
			return nil, err
		}
		if len(fs.Args()) > 0 {
			posArgs = append(posArgs, fs.Args()[0])
			remaining = fs.Args()[1:]
		} else {
			break
		}
	}
	return posArgs, nil
}
