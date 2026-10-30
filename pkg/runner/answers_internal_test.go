package runner

import "testing"

func TestAnAnswerInTheJobsLogNamesWhoGaveIt(t *testing.T) {
	if got := answeredBy("dev@example.com", "yes"); got != "dev@example.com answered yes: " {
		t.Errorf("answeredBy() = %q, want the person named", got)
	}
	if got := answeredBy("", "no"); got != "Answered no: " {
		t.Errorf("answeredBy() = %q, want no one claimed when the token names nobody", got)
	}
}
