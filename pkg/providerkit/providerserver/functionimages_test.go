package providerserver

import "testing"

func TestAFunctionsRepositoryIsANameARegistryTakes(t *testing.T) {
	got := functionRepository("web", "fn--web--index")
	if got != "web-index" {
		t.Errorf("functionRepository() = %q, want %q: a registry refuses a path component that repeats a separator", got, "web-index")
	}
	if only := functionRepository("web", "web"); only != "web" {
		t.Errorf("functionRepository() = %q, want the app itself where the function is the whole app", only)
	}
}
