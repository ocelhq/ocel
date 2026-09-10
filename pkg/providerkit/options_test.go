package providerkit

import (
	"errors"
	"strings"
	"testing"
)

func TestRefuseTransformsNamesTheVendorAndEveryModuleListed(t *testing.T) {
	t.Parallel()

	if err := RefuseTransforms("gcp", nil); err != nil {
		t.Fatalf("RefuseTransforms() = %v with nothing listed, want a deploy left alone", err)
	}

	err := RefuseTransforms("gcp", []string{"./transforms/network.transform.ts", "./transforms/tags.transform.ts"})
	if err == nil {
		t.Fatal("RefuseTransforms() = nil, want a deploy to a provider that renders nothing patchable refused")
	}
	for _, want := range []string{"gcp", "network.transform.ts", "tags.transform.ts"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("RefuseTransforms() = %v, want it to name %q", err, want)
		}
	}
}

type awsish struct {
	Region  string `json:"region"`
	Profile string `json:"profile,omitempty"`
	Retries int    `json:"retries,omitempty"`
}

func TestDecodeReadsTheVendorsOwnType(t *testing.T) {
	t.Parallel()

	got, err := Decode[awsish](Options{"region": "eu-west-1", "retries": 3})
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got.Region != "eu-west-1" || got.Retries != 3 {
		t.Errorf("Decode() = %+v", got)
	}
}

func TestDecodeRefusesAnUnknownOption(t *testing.T) {
	t.Parallel()

	_, err := Decode[awsish](Options{"region": "eu-west-1", "regoin": "typo"})
	var refusal Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Decode() error = %v, want a Refusal", err)
	}
	if refusal.Code != CodeUnknownOption {
		t.Errorf("Refusal.Code = %q, want %q", refusal.Code, CodeUnknownOption)
	}
	if !strings.Contains(refusal.Message, "regoin") {
		t.Errorf("Refusal.Message = %q, want it to name the option the CLI should print", refusal.Message)
	}
}

func TestDecodeRefusesANestedUnknownOptionByItsPath(t *testing.T) {
	t.Parallel()

	type ssh struct {
		Host string `json:"host"`
	}
	type nested struct {
		SSH ssh `json:"ssh"`
	}

	_, err := Decode[nested](Options{"ssh": map[string]any{"hostt": "example.com"}})
	var refusal Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Decode() error = %v, want a Refusal", err)
	}
	if !strings.Contains(refusal.Message, "provider.options.ssh.hostt") {
		t.Errorf("Refusal.Message = %q, want the option's path in the config", refusal.Message)
	}
}

func TestDecodeRefusesAnOptionOfTheWrongType(t *testing.T) {
	t.Parallel()

	_, err := Decode[awsish](Options{"region": 42})
	var refusal Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Decode() error = %v, want a Refusal", err)
	}
	if !strings.Contains(refusal.Message, "region") {
		t.Errorf("Refusal.Message = %q, want it to name the option", refusal.Message)
	}
}

func TestDecodeAcceptsNoOptionsAtAll(t *testing.T) {
	t.Parallel()

	if _, err := Decode[awsish](nil); err != nil {
		t.Fatalf("Decode(nil) error = %v, want the zero value", err)
	}
}
