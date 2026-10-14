package resolver

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"slices"
)

//go:embed minified/resolver.js
var code []byte

//go:embed src/gate.js
var gateSource []byte

//go:embed src/emptybody.js
var emptyBody []byte

func Code() []byte { return slices.Clone(code) }

func GateDigest() string {
	sum := sha256.Sum256(gateSource)
	return hex.EncodeToString(sum[:])
}

func EmptyBodyCode() []byte { return slices.Clone(emptyBody) }
