package resolver

import (
	_ "embed"
	"slices"
)

//go:embed src/resolver.js
var code []byte

//go:embed src/emptybody.js
var emptyBody []byte

func Code() []byte { return slices.Clone(code) }

func EmptyBodyCode() []byte { return slices.Clone(emptyBody) }
