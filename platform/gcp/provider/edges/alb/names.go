package alb

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

func Surface(namespace, slug string, tier environment.Tier) string {
	return naming.Join(naming.FieldSeparator, naming.NamespaceField(namespace), string(albresources.Kind), slug, naming.TierWord(tier))
}
