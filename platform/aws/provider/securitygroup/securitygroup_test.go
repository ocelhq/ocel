package securitygroup

import (
	"regexp"
	"strings"
	"testing"
)

var ec2AcceptedDescription = regexp.MustCompile(`^[a-zA-Z0-9. _\-:/()#,@\[\]+=&;{}!$*]{0,255}$`)

func TestFitDescriptionDropsEveryCharacterEC2Refuses(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"Ocel: the production tier's bastion":     "Ocel: the production tiers bastion",
		"shop / prod / web - the \"main\" db":     "shop / prod / web - the main db",
		"café — naïve`~^|<>?%'\\\"\t\n":           "caf  nave",
		"Ocel: ._-:/()#,@[]+=&;{}!$* stays as is": "Ocel: ._-:/()#,@[]+=&;{}!$* stays as is",
	}
	for described, want := range cases {
		got := FitDescription(described)
		if got != want {
			t.Errorf("FitDescription(%q) = %q, want %q", described, got, want)
		}
		if !ec2AcceptedDescription.MatchString(got) {
			t.Errorf("FitDescription(%q) = %q, which EC2 refuses", described, got)
		}
	}
}

func TestFitDescriptionCapsTheDescriptionAtTheLengthEC2Accepts(t *testing.T) {
	t.Parallel()

	got := FitDescription(strings.Repeat("it's ", 100))
	if len(got) != MaxDescriptionLen {
		t.Errorf("FitDescription of 500 characters is %d long, want %d", len(got), MaxDescriptionLen)
	}
	if !ec2AcceptedDescription.MatchString(got) {
		t.Errorf("FitDescription = %q, which EC2 refuses", got)
	}
}
