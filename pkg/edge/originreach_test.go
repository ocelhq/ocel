package edge

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/typescript"
)

func TestTheContractsOriginReachIsTheOneTheGoContractDeclares(t *testing.T) {
	typescript.RequireDeclared(t,
		filepath.Join("..", "..", "platform", "edge", "contract", "src", "origin-reach.mts"),
		typescript.Header("go test ./pkg/edge -run TestTheContractsOriginReachIsTheOneTheGoContractDeclares -update"),
		nil,
		reflect.TypeFor[OriginReach](),
	)
}

func TestAnOriginReachedOneWayIsComplete(t *testing.T) {
	t.Parallel()

	for name, reach := range map[string]OriginReach{
		"signed": {
			Reach:    Reach{Kind: ReachSigV4, Service: "lambda", Region: "eu-west-2", AccessKeyID: "AKIA", SecretAccessKey: "s"},
			Throttle: Throttle{Status: 429, Header: "x-amzn-errortype"},
		},
		"by a secret": {
			Reach:    Reach{Kind: ReachOriginSecret, Secret: "s1"},
			Throttle: Throttle{Status: 429, BodyContains: "no available instance"},
		},
		"by a secret during its rotation": {
			Reach:    Reach{Kind: ReachOriginSecret, Secret: "s2", PreviousSecret: "s1", PreviousFingerprint: OriginSecretFingerprint("s1")},
			Throttle: Throttle{Status: 429, BodyContains: "no available instance"},
		},
	} {
		if err := reach.RefuseIncomplete(); err != nil {
			t.Errorf("%s: RefuseIncomplete() = %v, want none", name, err)
		}
	}
}

func TestAnOriginReachThatLeavesTheEdgeGuessingIsRefused(t *testing.T) {
	t.Parallel()

	signed := Reach{Kind: ReachSigV4, Service: "lambda", Region: "eu-west-2", AccessKeyID: "AKIA", SecretAccessKey: "s"}
	throttled := Throttle{Status: 429}
	for name, reach := range map[string]OriginReach{
		"no reach":                                {Throttle: throttled},
		"an unknown reach":                        {Reach: Reach{Kind: "carrier-pigeon"}, Throttle: throttled},
		"signing with no key":                     {Reach: Reach{Kind: ReachSigV4, Service: "lambda", Region: "eu-west-2"}, Throttle: throttled},
		"a secret naming none":                    {Reach: Reach{Kind: ReachOriginSecret}, Throttle: throttled},
		"a secret and keys":                       {Reach: Reach{Kind: ReachOriginSecret, Secret: "s1", AccessKeyID: "AKIA", SecretAccessKey: "s"}, Throttle: throttled},
		"only a previous secret":                  {Reach: Reach{Kind: ReachOriginSecret, PreviousSecret: "s1", PreviousFingerprint: OriginSecretFingerprint("s1")}, Throttle: throttled},
		"a previous secret with no fingerprint":   {Reach: Reach{Kind: ReachOriginSecret, Secret: "s2", PreviousSecret: "s1"}, Throttle: throttled},
		"a previous fingerprint with no secret":   {Reach: Reach{Kind: ReachOriginSecret, Secret: "s2", PreviousFingerprint: OriginSecretFingerprint("s1")}, Throttle: throttled},
		"a previous secret under another's print": {Reach: Reach{Kind: ReachOriginSecret, Secret: "s2", PreviousSecret: "s1", PreviousFingerprint: OriginSecretFingerprint("s2")}, Throttle: throttled},
		"signing and a secret":                    {Reach: Reach{Kind: ReachSigV4, Service: "lambda", Region: "eu-west-2", AccessKeyID: "AKIA", SecretAccessKey: "s", Secret: "s1"}, Throttle: throttled},
		"a store with no key":                     {Reach: signed, Store: &OriginStore{URL: "https://store.example", Region: "auto"}, Throttle: throttled},
		"no throttle status":                      {Reach: signed},
	} {
		if err := reach.RefuseIncomplete(); err == nil {
			t.Errorf("%s: RefuseIncomplete() = nil, want the reach refused", name)
		}
	}
}

func TestAnOriginSecretsFingerprintIsTheFirst16HexDigitsOfItsSHA256(t *testing.T) {
	t.Parallel()

	for secret, want := range map[string]string{"current": "97b0560280ed60a5", "previous": "6da0633528deaa01"} {
		if got := OriginSecretFingerprint(secret); got != want {
			t.Errorf("OriginSecretFingerprint(%q) = %q, want %q: a release's record and the entry worker's previous secret carry these digits", secret, got, want)
		}
	}
}
