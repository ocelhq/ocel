package provider

import (
	"encoding/base64"
	"time"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/realtime"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type RealtimeSpec struct {
	Channels []ChannelSpec
	TokenTTL time.Duration
}

type ChannelSpec struct {
	Pattern   string
	Wildcard  bool
	Schema    string
	Subscribe realtime.ChannelAccess
	Publish   realtime.ChannelAccess
}

func verifyRealtimeProperties(binding Binding) error {
	for _, name := range []string{PropertySigningKey, PropertyVerifyKey} {
		if _, err := base64.StdEncoding.DecodeString(binding.Properties[name]); err != nil {
			return refusal.Refuse(refusal.CodeInvalid, "binding %s came back with a %s that is not base64", binding.Name, name)
		}
	}
	return nil
}

func realtimeProperties(binding Binding) *bindingsv1.RealtimeProperties {
	signingKey, _ := base64.StdEncoding.DecodeString(binding.Properties[PropertySigningKey])
	verifyKey, _ := base64.StdEncoding.DecodeString(binding.Properties[PropertyVerifyKey])
	return &bindingsv1.RealtimeProperties{
		Url:        binding.Properties[PropertyURL],
		Host:       binding.Properties[PropertyHost],
		SigningKey: signingKey,
		VerifyKey:  verifyKey,
	}
}
