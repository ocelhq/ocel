package configdoc

import "testing"

func TestAProviderNamesTheTextOptionsItCannotDeployWithout(t *testing.T) {
	required := RequiredProviderOptions("vps")
	if len(required) != 1 || required[0].Name != "ssh" || required[0].Doc == "" {
		t.Errorf("RequiredProviderOptions(vps) = %v, want ssh with what it is for", required)
	}
	if required := RequiredProviderOptions("aws"); len(required) != 0 {
		t.Errorf("RequiredProviderOptions(aws) = %v, want none: aws deploys with no options", required)
	}
}
