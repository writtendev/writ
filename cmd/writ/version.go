package main

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/internal/version"
)

type versionOpts struct {
	jsonMode bool
}

func newVersionFlagSet() (*flag.FlagSet, *versionOpts) {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	opts := &versionOpts{}
	fs.BoolVar(&opts.jsonMode, "json", false, "Output result as JSON")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"version"}, versionCmd)
	}
	return fs, opts
}

func runVersion(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs, opts := newVersionFlagSet()
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(posArgs) > 0 {
		porcelainf(stderr, "writ version: unexpected argument %q\n", posArgs[0])
		return 2
	}

	if opts.jsonMode {
		if err := emitJSON(stdout, wire.KindVersion, wire.Version{Version: version.Version}); err != nil {
			porcelainf(stderr, "writ version: marshal json: %v\n", err)
			return 1
		}
		return 0
	}
	porcelainf(stdout, "writ %s\n", version.Version)
	return 0
}
