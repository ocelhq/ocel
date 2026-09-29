package terminal

import (
	"bytes"
	"errors"
	"testing"
)

func TestAFailureOutsideAnyRunReadsLikeTheSummaryOfOneThatFailed(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	PrintFailure(&out, errors.New("no ocel.json found in this directory or any parent\nrun `ocel init` to set up this project"))

	want := "✗ no ocel.json found in this directory or any parent\n" +
		"  run `ocel init` to set up this project\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}
