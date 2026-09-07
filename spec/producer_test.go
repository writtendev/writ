package spec_test

import (
	"encoding/json"
	"testing"

	"github.com/writtendev/writ/spec"
)

// producerIndexEntry is the shape of one entry in
// spec/testdata/producer/index.json. The engine-level driver
// (engine/producer_corpus_test.go, package writ_test) reads the same file
// to drive both the producer verdict and the reader disposition — it
// cannot live here because package spec cannot import engine/codec
// (spec/schemasrc, engine/schema.go and engine/codec all sit above spec in
// the dependency graph). This file only pins that index.json and
// testdata/producer/cases name exactly the same set of files, the same
// pattern spec/testdata/*/invalid/index.json tests already use: a case
// file missing from the index would never be exercised by the engine-level
// driver (silently), and an index entry with no case file behind it would
// fail that same driver loudly on the missing read — but this test's job
// is to catch both directions here, at the source, rather than lean on the
// other package's incidental failure mode.
type producerIndexEntry struct {
	ProducerVerdict   string `json:"producer_verdict"`
	ReasonCode        string `json:"reason_code,omitempty"`
	ReaderDisposition string `json:"reader_disposition"`
	Reason            string `json:"reason"`
}

func TestProducerIndexAndCaseFilesAgree(t *testing.T) {
	rawIndex, err := spec.FS.ReadFile("testdata/producer/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]producerIndexEntry
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatalf("decoding index.json: %v", err)
	}
	if len(index) == 0 {
		t.Fatal("index.json lists no cases")
	}

	for file, entry := range index {
		if entry.ProducerVerdict != "accept" && entry.ProducerVerdict != "reject" {
			t.Errorf("%s: producer_verdict %q must be \"accept\" or \"reject\"", file, entry.ProducerVerdict)
		}
		if entry.ReaderDisposition != "interpretable" && entry.ReaderDisposition != "unknown-op" {
			t.Errorf("%s: reader_disposition %q must be \"interpretable\" or \"unknown-op\"", file, entry.ReaderDisposition)
		}
		if entry.Reason == "" {
			t.Errorf("%s: index entry has no reason", file)
		}
	}

	caseFiles := make(map[string]bool)
	for _, name := range readDirNames(t, "testdata/producer/cases") {
		caseFiles[name] = true
		if _, ok := index[name]; !ok {
			t.Errorf("case file %s is not listed in testdata/producer/index.json", name)
		}
	}

	for file := range index {
		if !caseFiles[file] {
			t.Errorf("index.json entry %s has no case file under testdata/producer/cases", file)
		}
	}
}
