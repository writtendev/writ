// Package sshsig implements pure-Go parsing and verification of OpenSSH SSH signatures
// conforming to PROTOCOL.sshsig.
package sshsig

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	// Magic is the 6-byte preamble identifying an SSHSIG buffer.
	Magic = "SSHSIG"

	// Version is the supported SSHSIG format version.
	Version uint32 = 1

	// ArmorHeader is the standard OpenSSH armored signature header.
	ArmorHeader = "-----BEGIN SSH SIGNATURE-----"

	// ArmorFooter is the standard OpenSSH armored signature footer.
	ArmorFooter = "-----END SSH SIGNATURE-----"
)

// Signature represents a parsed OpenSSH signature blob per PROTOCOL.sshsig.
type Signature struct {
	Version       uint32
	PublicKey     ssh.PublicKey
	RawPublicKey  []byte
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Signature     *ssh.Signature
}

type sshsigFields struct {
	PublicKey     []byte
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Signature     []byte
}

type signedDataFields struct {
	Namespace     string
	Reserved      string
	HashAlgorithm string
	HMessage      string
}

// Unarmor extracts and decodes the base64 payload from an armored SSHSIG string.
func Unarmor(armored string) ([]byte, error) {
	var b64 strings.Builder
	inArmor := false
	sawHeader := false
	sawFooter := false

	for _, line := range strings.Split(armored, "\n") {
		line = strings.TrimSpace(line)
		if line == ArmorHeader {
			inArmor = true
			sawHeader = true
			continue
		}
		if line == ArmorFooter {
			sawFooter = true
			break
		}
		if inArmor {
			b64.WriteString(line)
		}
	}

	if !sawHeader || !sawFooter || b64.Len() == 0 {
		return nil, errors.New("sshsig: missing or malformed armor delimiters")
	}

	decoded, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		return nil, fmt.Errorf("sshsig: invalid base64 encoding: %w", err)
	}

	return decoded, nil
}

// ParseSignature parses an armored SSHSIG string into a Signature.
func ParseSignature(armored string) (*Signature, error) {
	raw, err := Unarmor(armored)
	if err != nil {
		return nil, err
	}
	return ParseRawSignature(raw)
}

// ParseRawSignature parses raw SSHSIG binary wire bytes into a Signature.
func ParseRawSignature(raw []byte) (*Signature, error) {
	if len(raw) < len(Magic)+4 {
		return nil, errors.New("sshsig: blob too short")
	}

	if string(raw[:len(Magic)]) != Magic {
		return nil, errors.New("sshsig: invalid magic preamble")
	}

	version := binary.BigEndian.Uint32(raw[len(Magic) : len(Magic)+4])
	if version != Version {
		return nil, fmt.Errorf("sshsig: unsupported version %d (expected %d)", version, Version)
	}

	var fields sshsigFields
	if err := ssh.Unmarshal(raw[len(Magic)+4:], &fields); err != nil {
		return nil, fmt.Errorf("sshsig: unmarshal fields: %w", err)
	}

	pubKey, err := ssh.ParsePublicKey(fields.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("sshsig: parse public key: %w", err)
	}

	var innerSig ssh.Signature
	if err := ssh.Unmarshal(fields.Signature, &innerSig); err != nil {
		return nil, fmt.Errorf("sshsig: parse inner signature: %w", err)
	}

	return &Signature{
		Version:       version,
		PublicKey:     pubKey,
		RawPublicKey:  fields.PublicKey,
		Namespace:     fields.Namespace,
		Reserved:      fields.Reserved,
		HashAlgorithm: fields.HashAlgorithm,
		Signature:     &innerSig,
	}, nil
}

// Verify verifies the signature against message and the expected namespace.
func Verify(sig *Signature, message []byte, expectedNamespace string) error {
	if sig == nil {
		return errors.New("sshsig: nil signature")
	}
	if expectedNamespace != "" && sig.Namespace != expectedNamespace {
		return fmt.Errorf("sshsig: namespace mismatch: expected %q, got %q", expectedNamespace, sig.Namespace)
	}

	var hashDigest []byte
	switch strings.ToLower(sig.HashAlgorithm) {
	case "sha512":
		h := sha512.Sum512(message)
		hashDigest = h[:]
	case "sha256":
		h := sha256.Sum256(message)
		hashDigest = h[:]
	default:
		return fmt.Errorf("sshsig: unsupported hash algorithm: %q", sig.HashAlgorithm)
	}

	var signedData []byte
	signedData = append(signedData, []byte(Magic)...)
	signedData = append(signedData, ssh.Marshal(signedDataFields{
		Namespace:     sig.Namespace,
		Reserved:      sig.Reserved,
		HashAlgorithm: sig.HashAlgorithm,
		HMessage:      string(hashDigest),
	})...)

	if err := sig.PublicKey.Verify(signedData, sig.Signature); err != nil {
		return fmt.Errorf("sshsig: signature verification failed: %w", err)
	}

	return nil
}

// KeyFingerprint returns the SHA256 fingerprint for a public key ("SHA256:<base64-unpadded>").
func KeyFingerprint(pubKey ssh.PublicKey) string {
	if pubKey == nil {
		return ""
	}
	return FingerprintFromBytes(pubKey.Marshal())
}

// FingerprintFromBytes returns the SHA256 fingerprint for raw public key wire bytes.
func FingerprintFromBytes(pubKeyBytes []byte) string {
	if len(pubKeyBytes) == 0 {
		return ""
	}
	h := sha256.Sum256(pubKeyBytes)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:])
}

// FingerprintFromSignature parses an armored SSH signature and returns its key fingerprint
// even if full signature verification has not yet run.
func FingerprintFromSignature(armored string) string {
	sig, err := ParseSignature(armored)
	if err != nil || sig.PublicKey == nil {
		return ""
	}
	return KeyFingerprint(sig.PublicKey)
}
