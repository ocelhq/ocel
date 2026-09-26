package providerserver

import (
	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func envName(env *environmentv1.Environment) (string, error) {
	class, err := classOf(env.GetTier())
	if err != nil {
		return "", err
	}
	if class == edge.ClassProduction {
		return stackrecords.ProductionEnv, nil
	}
	identity := env.GetIdentity()
	if identity == "" {
		return "", refusal.Refuse(refusal.CodeInvalid, "a preview environment is addressed by its identity, and this call names none")
	}
	if err := naming.Validate("preview name", identity); err != nil {
		return "", refusal.Refuse(refusal.CodeInvalid, "%s", err.Error())
	}
	if identity == stackrecords.ProductionEnv {
		return "", refusal.Refuse(refusal.CodeInvalid, "%q names production, so it is not a preview environment's identity", identity)
	}
	return identity, nil
}

func lifecycleOf(persisted bool) environmentv1.Lifecycle {
	if persisted {
		return environmentv1.Lifecycle_LIFECYCLE_PERSISTENT
	}
	return environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL
}
