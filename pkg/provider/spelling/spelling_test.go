package spelling_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider/spelling"
)

func TestFindClosestNamesTheCandidateAValueWasMeantToSpell(t *testing.T) {
	t.Parallel()

	gcp := []string{"europe-west1", "us-central1", "us-east1", "us-east4"}
	aws := []string{"eu-west-1", "eu-west-2", "us-east-1", "us-west-2"}
	for _, c := range []struct {
		name       string
		value      string
		candidates []string
		want       string
	}{
		{name: "a zone of a region", value: "us-central1-f", candidates: gcp, want: "us-central1"},
		{name: "a letter dropped", value: "us-centrl1", candidates: gcp, want: "us-central1"},
		{name: "two letters swapped", value: "eu-wset-1", candidates: aws, want: "eu-west-1"},
		{name: "an availability zone", value: "us-west-2a", candidates: aws, want: "us-west-2"},
		{name: "a local zone within a region", value: "us-west-2-lax-1a", candidates: aws, want: "us-west-2"},
		{name: "another case", value: "US-Central1", candidates: gcp, want: "us-central1"},
		{name: "one of several equally close", value: "us-east", candidates: gcp, want: "us-east1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, ok := spelling.FindClosest(c.value, c.candidates)
			if !ok || got != c.want {
				t.Errorf("FindClosest(%q) = %q, %v, want %q", c.value, got, ok, c.want)
			}
		})
	}
}

func TestFindClosestNamesNothingForAValueNoCandidateResembles(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"no-such-region1", "us", "", "asia-south1"} {
		if got, ok := spelling.FindClosest(value, []string{"europe-west1", "us-central1", "us-east1"}); ok {
			t.Errorf("FindClosest(%q) = %q, want no candidate named", value, got)
		}
	}
}

func TestSuggestClosestAsksAfterTheCandidateMeantAndOnlyThen(t *testing.T) {
	t.Parallel()

	regions := []string{"us-central1", "us-east1"}
	if got, want := spelling.SuggestClosest("us-central1-f", regions), ` Did you mean "us-central1"?`; got != want {
		t.Errorf("SuggestClosest(us-central1-f) = %q, want %q", got, want)
	}
	if got := spelling.SuggestClosest("no-such-region1", regions); got != "" {
		t.Errorf("SuggestClosest(no-such-region1) = %q, want nothing asked", got)
	}
}
