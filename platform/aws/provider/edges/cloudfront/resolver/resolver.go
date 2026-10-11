package resolver

import (
	_ "embed"
	"slices"
)

//go:embed src/resolver.js
var code []byte

//go:embed src/ungated.js
var ungated []byte

//go:embed minified/preview.js
var preview []byte

//go:embed src/emptybody.js
var emptyBody []byte

func Code() []byte { return slices.Concat(code, []byte("\n"), ungated) }

func PreviewCode() []byte { return slices.Clone(preview) }

func EmptyBodyCode() []byte { return slices.Clone(emptyBody) }
