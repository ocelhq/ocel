package resolver

import (
	_ "embed"
	"slices"
)

//go:embed src/resolver.js
var code []byte

func Code() []byte { return slices.Clone(code) }
