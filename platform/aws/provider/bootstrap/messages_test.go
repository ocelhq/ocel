package bootstrap

import (
	"context"
	"slices"
	"strings"
	"testing"
	"unicode"

	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestABootstrapRunSaysWhatItWroteAndKeepsWhatItReusedAtDebug(t *testing.T) {
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	frontedBy(t, &fakeEdge{kind: "cloudflare"})
	apis := apisOf(stacks, ssmc, iamc, preloadedStore())

	var first, again fake.Log
	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, everything(), &first); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, everything(), &again); err != nil {
		t.Fatalf("Run again: %v", err)
	}

	for _, want := range []string{
		"INFO Minted a new production origin secret",
		"INFO Generated a new Pulumi passphrase",
		"INFO Minted a new edge reader access key",
		"INFO Applied stack " + isrStack(environment.TierProduction),
		"INFO Applied stack " + runtimeStack(environment.TierProduction),
	} {
		if !slices.Contains(first.Lines(), want) {
			t.Errorf("the first bootstrap said %v, want %q", first.Lines(), want)
		}
	}
	for _, want := range []string{
		"DEBUG Reused the existing production origin secret",
		"DEBUG Reused the existing Pulumi passphrase",
		"DEBUG Reused the existing edge reader access key",
	} {
		if !slices.Contains(again.Lines(), want) {
			t.Errorf("the second bootstrap said %v, want %q", again.Lines(), want)
		}
	}
	assertSentences(t, append(first.Lines(), again.Lines()...))
}

func TestATeardownSaysWhatItRemovedInSentences(t *testing.T) {
	apis, _, _, _ := teardownFakes(t)
	var progress fake.Log

	if err := Teardown(context.Background(), apis, defaultNamespace, environment.TierProduction, &progress); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if !slices.Contains(progress.Lines(), "INFO Emptying bucket ocel-state") {
		t.Errorf("teardown said %v, want each bucket it empties named as a bucket", progress.Lines())
	}
	assertSentences(t, progress.Lines())
}

func TestAWriteThatReplacesAResourceWarnsAndNamesIt(t *testing.T) {
	var progress fake.Log
	stack := isrStack(environment.TierProduction)

	if err := AdmitReplacements(defaultNamespace, true, &progress)(stack, []cfntypes.ResourceChange{
		change(cfntypes.ChangeActionModify, "RevalidateQueue", "AWS::SQS::Queue", cfntypes.ReplacementTrue),
	}); err != nil {
		t.Fatalf("an accepted replacement was refused: %v", err)
	}
	want := []string{"WARN Stack " + stack + " would replace RevalidateQueue (AWS::SQS::Queue) rather than update it in place"}
	if got := progress.Lines(); !slices.Equal(got, want) {
		t.Errorf("the review said %v, want %v", got, want)
	}
}

func assertSentences(t *testing.T, lines []string) {
	t.Helper()
	for _, line := range lines {
		level, text, _ := strings.Cut(line, " ")
		if level == "OUTPUT" {
			t.Errorf("%q is verbatim output, want a leveled message: a bootstrap runs no tool whose lines it relays", line)
		}
		if first := []rune(text); len(first) == 0 || !unicode.IsUpper(first[0]) {
			t.Errorf("%q does not open a sentence", line)
		}
	}
}
