package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
)

// Fingerprint accumulates the labeled parts of a stack observation into
// one hash. A Transaction plans against a fingerprint and refuses to
// apply if it has changed, so the hash must not depend on the order the
// caller adds parts in beyond what the caller controls, and must not let
// two different part lists collide: every part is length-prefixed.
type Fingerprint struct {
	hash hash.Hash
}

// NewFingerprint starts an empty fingerprint.
func NewFingerprint() *Fingerprint {
	return &Fingerprint{hash: sha256.New()}
}

// Add mixes in one labeled string part.
func (f *Fingerprint) Add(label, value string) {
	f.AddBytes(label, []byte(value))
}

// AddBytes mixes in one labeled byte part.
func (f *Fingerprint) AddBytes(label string, value []byte) {
	fmt.Fprintf(f.hash, "%d:%s=%d:", len(label), label, len(value))
	f.hash.Write(value)
}

// Sum renders the fingerprint.
func (f *Fingerprint) Sum() string {
	return hex.EncodeToString(f.hash.Sum(nil))
}
