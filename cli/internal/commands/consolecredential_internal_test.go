package commands

import "testing"

func TestTheIssuedCredentialLineNamesARegionOnlyWhenThereIsOne(t *testing.T) {
	if got, want := issuedCredentialLine("production", "box.example.com", ""), "Deploying production to account box.example.com with a credential the console issued."; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	if got, want := issuedCredentialLine("production", "123456789012", "eu-west-1"), "Deploying production to account 123456789012 in eu-west-1 with a credential the console issued."; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}
