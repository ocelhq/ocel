package naming

import (
	"maps"
	"slices"
	"strings"
)

const (
	TokenNamespace = "ocel:"

	TokenPostgres = TokenNamespace + "postgres"
	TokenBucket   = TokenNamespace + "bucket"
)

var tokenKinds = map[string]Kind{
	TokenPostgres: KindDatabase,
	TokenBucket:   KindBucket,
}

func Tokens() []string {
	return slices.Sorted(maps.Keys(tokenKinds))
}

func TokenKind(token string) (Kind, bool) {
	kind, ok := tokenKinds[token]
	return kind, ok
}

func EnvFragment(token string) string {
	return strings.ToUpper(strings.TrimPrefix(token, TokenNamespace))
}
