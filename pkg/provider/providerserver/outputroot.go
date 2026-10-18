package providerserver

import (
	"path/filepath"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func readOutputRoot(req *contractv1.DeployRequest) (string, error) {
	root := req.GetOutputRoot()
	if len(req.GetManifest().GetApps()) == 0 || filepath.IsAbs(root) {
		return root, nil
	}
	return "", refusal.Refuse(refusal.CodeInvalid,
		"this deploy ships %d apps and names the build output root %q, which is not an absolute directory: the provider reads each app's build from the root the deploy names",
		len(req.GetManifest().GetApps()), root)
}
