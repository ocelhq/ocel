package providerkit

import (
	"errors"
	"strings"
	"testing"
)

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
	if refusal.Code != CodeInvalid {
		t.Errorf("Refusal.Code = %q, want %q", refusal.Code, CodeInvalid)
	}
	if !strings.Contains(refusal.Message, "regoin") {
		t.Errorf("Refusal.Message = %q, want it to name the option the CLI should print", refusal.Message)
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
