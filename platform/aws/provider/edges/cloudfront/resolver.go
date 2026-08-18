package cloudfront

import (
	_ "embed"
	"slices"
)

//go:embed resolver/src/resolver.js
var resolverCode []byte

func ResolverCode() []byte { return slices.Clone(resolverCode) }
