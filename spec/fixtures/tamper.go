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
	TamperArmorRewrap    = "armor-rewrap"
	TamperSigTransplant  = "signature-transplant"
)

var validTamperEnums = map[string]bool{
	TamperPayloadByte:    true,
	TamperMessage:        true,
	TamperAuthor:         true,
	TamperSignature:      true,
	TamperOpJsonModeExec: true,
	TamperArmorRewrap:    true,
	TamperSigTransplant:  true,
}

// IsValidTamper reports whether tamper is a recognized closed tamper enum value.
func IsValidTamper(tamper string) bool {
	return validTamperEnums[tamper]
}

// applyTamper modifies the commit object or its tree after signing according
// to the specified tamper mode, preserving the original signature (unless
// mutating the signature itself).
//
// sgnr and signerId are the signer and identity the commit was (or, were it
// signed, would have been) signed with; only signature-transplant uses them.
func applyTamper(store storer.EncodedObjectStorer, sgnr *signer, signerId identity, commit *object.Commit, files map[string]string, tamper string) error {
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

	case TamperArmorRewrap:
		if commit.PGPSignature == "" {
			return fmt.Errorf("tamper armor-rewrap: commit is unsigned")
		}
		rewrapped, err := rewrapArmor(commit.PGPSignature, armorRewrapWidth)
		if err != nil {
			return fmt.Errorf("tamper armor-rewrap: %w", err)
		}
		commit.PGPSignature = rewrapped

	case TamperSigTransplant:
		// A real, valid SSHSIG by the signing identity, but over another
		// commit's payload: this commit's own payload with its message
		// altered. The commit's signed bytes are untouched, so it is the
		// same op as an untampered copy, but its signature does not cover
		// them: payload-mutated, attributed to the signer's key.
		other := *commit
		other.PGPSignature = ""
		other.Message = strings.TrimSuffix(commit.Message, "\n") + " [transplanted from another commit]\n"
		transplanted, err := signCommit(store, sgnr, signerId, &other)
		if err != nil {
			return fmt.Errorf("tamper signature-transplant: %w", err)
		}
		commit.PGPSignature = transplanted

	default:
		return fmt.Errorf("unknown tamper mode: %q", tamper)
	}
	return nil
}

// armorRewrapWidth is the column width armor-rewrap re-wraps a signature's
// base64 body at. ssh-keygen -Y sign emits 70; anything else gives the same
// signature bytes under a different commit SHA.
const armorRewrapWidth = 40

// rewrapArmor re-wraps the base64 body of an armored SSH signature at width
// columns, keeping the header, footer, and trailing newline (or lack of
// one). The decoded signature bytes are unchanged.
func rewrapArmor(armored string, width int) (string, error) {
	const header, footer = "-----BEGIN SSH SIGNATURE-----", "-----END SSH SIGNATURE-----"
	trailing := ""
	if strings.HasSuffix(armored, "\n") {
		trailing = "\n"
	}
	var body strings.Builder
	var sawHeader, sawFooter bool
	for _, line := range strings.Split(armored, "\n") {
		switch line = strings.TrimSpace(line); {
		case line == header:
			sawHeader = true
		case line == footer:
			sawFooter = true
		case sawHeader && !sawFooter:
			body.WriteString(line)
		}
	}
	if !sawHeader || !sawFooter || body.Len() == 0 {
		return "", fmt.Errorf("signature is not armored SSH signature")
	}
	b64 := body.String()
	var out strings.Builder
	out.WriteString(header + "\n")
	for len(b64) > 0 {
		n := min(width, len(b64))
		out.WriteString(b64[:n] + "\n")
		b64 = b64[n:]
	}
	out.WriteString(footer + trailing)
	return out.String(), nil
}
