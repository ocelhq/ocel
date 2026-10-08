package edge

import "testing"

func TestAStaticPathIsImmutableOnlyUnderAnImmutablePrefixAndOutsideEveryMustRevalidatePrefix(t *testing.T) {
	t.Parallel()

	static := &Static{
		ImmutablePrefixes:      []string{"/docs/_next/static/"},
		MustRevalidatePrefixes: []string{"/docs/_next/static/service-worker/"},
	}
	for path, want := range map[string]bool{
		"/docs/_next/static/chunks/main.js":       true,
		"/docs/_next/static/service-worker/sw.js": false,
		"/_next/static/chunks/main.js":            false,
		"/docs/favicon.ico":                       false,
	} {
		if got := static.IsImmutable(path); got != want {
			t.Errorf("IsImmutable(%q) = %v, want %v", path, got, want)
		}
	}
	if (*Static)(nil).IsImmutable("/docs/_next/static/chunks/main.js") {
		t.Error("a build with no static dir calls a path immutable")
	}
}
