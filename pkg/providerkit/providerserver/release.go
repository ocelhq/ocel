package providerserver

import (
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
)

func frameworkOf(fn *contractv1.ManifestFunction) appbuild.Framework {
	return appbuild.Framework{Name: fn.GetFramework().GetName(), Arch: fn.GetFramework().GetArch()}
}
