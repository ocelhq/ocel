package deploy

import "testing"

func TestSafeNameKeepsLongNamesDistinct(t *testing.T) {
	const shared = "my-very-long-project-name-that-runs-past-the-limit"

	a := safeName(shared + "-alpha")
	b := safeName(shared + "-beta")

	if a == b {
		t.Fatalf("safeName collapsed two distinct slugs to %q — teardown filters on this value and would plan across projects", a)
	}
	for _, got := range []string{a, b} {
		if len(got) > maxSafeNamePrefixLen {
			t.Errorf("safeName(%q) = %q, length %d exceeds %d", shared, got, len(got), maxSafeNamePrefixLen)
		}
	}
	if again := safeName(shared + "-alpha"); again != a {
		t.Errorf("safeName is not deterministic: %q then %q", a, again)
	}
}

func TestStateBackendURLSeparatesCollidingSlugs(t *testing.T) {
	const shared = "another-extremely-long-slug-that-will-be-truncated"

	if one, two := StateBackendURL("b", shared+"-one"), StateBackendURL("b", shared+"-two"); one == two {
		t.Fatalf("two slugs share the state subpath %q", one)
	}
}
