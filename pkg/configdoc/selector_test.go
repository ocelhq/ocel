package configdoc

import (
	"fmt"
	"slices"
	"testing"
)

func TestAProviderNamesTheTextOptionsItCannotDeployWithout(t *testing.T) {
	required := RequiredProviderOptions("vps")
	if len(required) != 1 || required[0].Name != "ssh" || required[0].Doc == "" {
		t.Errorf("RequiredProviderOptions(vps) = %v, want ssh with what it is for", required)
	}
	if required := RequiredProviderOptions("aws"); len(required) != 0 {
		t.Errorf("RequiredProviderOptions(aws) = %v, want none: aws deploys with no options", required)
	}
}

func TestAProviderNamesEveryOptionItTakesAsTextAndWhichItCannotDeployWithout(t *testing.T) {
	var named []string
	for _, option := range TextProviderOptions("gcp") {
		named = append(named, fmt.Sprintf("%s required=%t", option.Name, option.Required))
	}
	if want := []string{"project required=false", "region required=true"}; !slices.Equal(named, want) {
		t.Errorf("TextProviderOptions(gcp) = %v, want %v: previewViewers is a list, not text", named, want)
	}
}
