package fixtures

import (
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

const (
	TamperPayloadByte    = "payload-byte"
	TamperMessage        = "message"
	TamperAuthor         = "author"
	TamperSignature      = "signature"
	TamperOpJsonModeExec = "op-json-mode-exec"
)

var validTamperEnums = map[string]bool{
	TamperPayloadByte:    true,
	TamperMessage:        true,
	TamperAuthor:         true,
	TamperSignature:      true,
	TamperOpJsonModeExec: true,
}

// IsValidTamper reports whether tamper is a recognized closed tamper enum value.
func IsValidTamper(tamper string) bool {
	return validTamperEnums[tamper]
}

// applyTamper modifies the commit object or its tree after signing according
// to the specified tamper mode, preserving the original signature (unless
// mutating the signature itself).
func applyTamper(store storer.EncodedObjectStorer, commit *object.Commit, files map[string]string, tamper string) error {
	switch tamper {
	case TamperPayloadByte:
		mutatedFiles := make(map[string]string, len(files))
		for k, v := range files {
			mutatedFiles[k] = v
		}
		if opContent, ok := mutatedFiles["op.json"]; ok {
			if strings.Contains(opContent, "Payload") {
				mutatedFiles["op.json"] = strings.Replace(opContent, "Payload", "Bayload", 1)
			} else if strings.Contains(opContent, "widget") {
				mutatedFiles["op.json"] = strings.Replace(opContent, "widget", "widgat", 1)
			} else {
				mutatedFiles["op.json"] = opContent + " "
			}
		} else {
			for k, v := range mutatedFiles {
				mutatedFiles[k] = v + " "
				break
			}
		}
		newTreeHash, err := buildTree(store, mutatedFiles)
		if err != nil {
			return fmt.Errorf("tamper payload-byte: build tree: %w", err)
		}
		commit.TreeHash = newTreeHash

	case TamperMessage:
		commit.Message = strings.TrimSuffix(commit.Message, "\n") + " [tampered]\n"

	case TamperAuthor:
		commit.Author.Name = commit.Author.Name + " (Tampered)"

	case TamperSignature:
		if commit.PGPSignature != "" {
			commit.PGPSignature = strings.Replace(commit.PGPSignature, "BEGIN SSH SIGNATURE", "CORRUPTED SSH SIGNATURE", 1)
		}

	case TamperOpJsonModeExec:
		modes := map[string]filemode.FileMode{
			"op.json": filemode.Executable,
		}
		newTreeHash, err := buildTreeWithModes(store, files, modes)
		if err != nil {
			return fmt.Errorf("tamper op-json-mode-exec: build tree: %w", err)
		}
		commit.TreeHash = newTreeHash

	default:
		return fmt.Errorf("unknown tamper mode: %q", tamper)
	}
	return nil
}
