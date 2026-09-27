package runui

import (
	"strings"
	"testing"
)

func TestAQuotedListOfThreeReadsAsASentence(t *testing.T) {
	t.Parallel()

	if got, want := Quoted([]string{"a", "b", "c"}), `"a", "b" and "c"`; got != want {
		t.Errorf("Quoted() = %s, want %s", got, want)
	}
	if got, want := Quoted([]string{"a", "b"}), `"a" and "b"`; got != want {
		t.Errorf("Quoted() = %s, want %s", got, want)
	}
	if got, want := Quoted([]string{"a"}), `"a"`; got != want {
		t.Errorf("Quoted() = %s, want %s", got, want)
	}
}

func TestAListOfNamesReadsAsASentence(t *testing.T) {
	t.Parallel()

	for names, want := range map[string]string{"": "", "web": "web", "web api": "web and api", "web api jobs": "web, api and jobs"} {
		if got := Listed(strings.Fields(names)); got != want {
			t.Errorf("Listed(%q) = %q, want %q", names, got, want)
		}
	}
}
