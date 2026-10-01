package provider

import (
	"encoding/base64"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func verifyRealtimeProperties(binding Binding) error {
	transport, known := bindingsv1.RealtimeTransport_value[binding.Properties[PropertyTransport]]
	if !known || transport == int32(bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_UNSPECIFIED) {
		return refusal.Refuse(refusal.CodeInvalid, "binding %s came back with transport %q, which no realtime client speaks",
			binding.Name, binding.Properties[PropertyTransport])
	}
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
		Transport:  bindingsv1.RealtimeTransport(bindingsv1.RealtimeTransport_value[binding.Properties[PropertyTransport]]),
		Url:        binding.Properties[PropertyURL],
		Host:       binding.Properties[PropertyHost],
		SigningKey: signingKey,
		VerifyKey:  verifyKey,
	}
}
