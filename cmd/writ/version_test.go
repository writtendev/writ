package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/internal/version"
)

func TestVersion_Porcelain(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("version exited with %d; stderr: %s", code, stderr.String())
	}
	if want := "writ " + version.Version + "\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestVersion_JSON(t *testing.T) {
	for _, flagArg := range []string{"--json", "-json"} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"version", flagArg}, &stdout, &stderr); code != 0 {
			t.Fatalf("version %s exited with %d; stderr: %s", flagArg, code, stderr.String())
		}
		dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
		var env wire.Envelope
		var data wire.Version
		env.Data = &data
		if err := dec.Decode(&env); err != nil {
			t.Fatalf("decode version %s: %v (raw: %s)", flagArg, err, stdout.String())
		}
		if dec.More() {
			t.Errorf("version %s: stdout carries more than one JSON document: %s", flagArg, stdout.String())
		}
		if env.SchemaVersion != wire.CurrentSchemaVersion || env.Kind != wire.KindVersion || data.Version != version.Version {
			t.Errorf("version %s: envelope = %+v, data = %+v, want kind %q with version %q", flagArg, env, data, wire.KindVersion, version.Version)
		}
	}
}

func TestVersion_UsageErrorsAndHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"version", "extra"}, &stdout, &stderr); code != 2 {
		t.Errorf("version extra exited with %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("version extra wrote to stdout: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"version", "-h"}, &stdout, &stderr); code != 0 {
		t.Errorf("version -h exited with %d, want 0", code)
	}
	if !strings.Contains(stdout.String()+stderr.String(), "Usage: writ version [--json]") {
		t.Errorf("version -h did not print usage: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}
