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
		"certified": {
			Reach:    Reach{Kind: ReachClientCertificate, Certificate: "c1"},
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
		"no reach":                   {Throttle: throttled},
		"an unknown reach":           {Reach: Reach{Kind: "carrier-pigeon"}, Throttle: throttled},
		"signing with no key":        {Reach: Reach{Kind: ReachSigV4, Service: "lambda", Region: "eu-west-2"}, Throttle: throttled},
		"signing and a certificate":  {Reach: Reach{Kind: ReachSigV4, Service: "lambda", Region: "eu-west-2", AccessKeyID: "AKIA", SecretAccessKey: "s", Certificate: "c1"}, Throttle: throttled},
		"a certificate naming none":  {Reach: Reach{Kind: ReachClientCertificate}, Throttle: throttled},
		"a certificate and keys":     {Reach: Reach{Kind: ReachClientCertificate, Certificate: "c1", AccessKeyID: "AKIA"}, Throttle: throttled},
		"a store with no key":        {Reach: signed, Store: &OriginStore{URL: "https://store.example", Region: "auto"}, Throttle: throttled},
		"entries a store holds none": {Reach: signed, Store: &OriginStore{URL: "https://store.example", Region: "auto", AccessKeyID: "a", SecretAccessKey: "s", EntriesPrefix: "cache/"}, Throttle: throttled},
		"no throttle status":         {Reach: signed},
	} {
		if err := reach.RefuseIncomplete(); err == nil {
			t.Errorf("%s: RefuseIncomplete() = nil, want the reach refused", name)
		}
	}
}
