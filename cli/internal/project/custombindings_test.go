package project

import (
	"slices"
	"strings"
	"testing"
)

func TestAnAppIsBoundToTheCustomRecordsItNames(t *testing.T) {
	t.Parallel()

	cfg := mustLoadJSON(t, `{"slug":"cloud","apps":[
		{"name":"api","path":"api","framework":"go","bindings":{"custom":["@fleet","@builds"]}},
		{"name":"runtime","path":"runtime","framework":"go"}
	]}`)

	if got, want := cfg.Apps[0].CustomBindings, []string{"builds", "fleet"}; !slices.Equal(got, want) {
		t.Errorf("api is bound to custom records %v, want %v", got, want)
	}
	if got := cfg.Apps[1].CustomBindings; got != nil {
		t.Errorf("runtime is bound to custom records %v, want none: it names none", got)
	}
}

func TestACustomBindingNamedWithoutItsSigilIsRefused(t *testing.T) {
	t.Parallel()

	_, err := loadJSON(t, `{"slug":"cloud","apps":[{"name":"api","path":"api","framework":"go","bindings":{"custom":["fleet"]}}]}`)
	if err == nil || !strings.Contains(err.Error(), `"@fleet"`) {
		t.Errorf("load = %v, want the record name refused, asking for \"@fleet\"", err)
	}
}

func TestACustomBindingNamedTwiceIsRefused(t *testing.T) {
	t.Parallel()

	_, err := loadJSON(t, `{"slug":"cloud","apps":[{"name":"api","path":"api","framework":"go","bindings":{"custom":["@fleet","@fleet"]}}]}`)
	if err == nil || !strings.Contains(err.Error(), "fleet") {
		t.Errorf("load = %v, want the repeated record refused", err)
	}
}
