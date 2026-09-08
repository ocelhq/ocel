package providerkit

import "testing"

func TestAFunctionsRouteIsWhatIsLeftOfItsLogicalName(t *testing.T) {
	if got, want := FunctionRoute("web", "fn--web--index"), "index"; got != want {
		t.Errorf("FunctionRoute() = %q, want %q: the app is already named beside it", got, want)
	}
	if got, want := FunctionRoute("web", "fn--web--api-users"), "api-users"; got != want {
		t.Errorf("FunctionRoute() = %q, want %q", got, want)
	}
	if got, want := FunctionRoute("web", "web"), "web"; got != want {
		t.Errorf("FunctionRoute() = %q, want %q: a name that is no coordinate is left alone", got, want)
	}
	if got, want := FunctionRoute("web", "fn--web--API/Users_[id]"), "api-users-id"; got != want {
		t.Errorf("FunctionRoute() = %q, want %q: every caller names a resource with it, and no registry, "+
			"bucket or service takes what a route may hold", got, want)
	}
}

func TestAFunctionsRepositoryIsANameARegistryTakes(t *testing.T) {
	got := functionRepository("web", "fn--web--index")
	if got != "web-index" {
		t.Errorf("functionRepository() = %q, want %q: a registry refuses a path component that repeats a separator", got, "web-index")
	}
	if only := functionRepository("web", "web"); only != "web" {
		t.Errorf("functionRepository() = %q, want the app itself where the function is the whole app", only)
	}
}
