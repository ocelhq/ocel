package runui

import "testing"

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
