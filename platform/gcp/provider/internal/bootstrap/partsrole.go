package bootstrap

import (
	"slices"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const servicesReadPermission = "serviceusage.services.get"

func NewPartsRole(names names.Names) customRole {
	return customRole{
		ID:          names.PartsRole(),
		title:       "ocel bootstrap parts (" + string(names.Namespace()) + ")",
		description: "lets a deploy add the parts of a bootstrap it needs on first use, where Google offers no condition to name them by, and nothing else",
		Permissions: slices.Concat([]string{servicesReadPermission}, networkPartPermissions),
		DriftReason: "its permissions drifted",
	}
}

var networkPartPermissions = []string{
	"compute.networks.create",
	"compute.networks.get",
	"compute.networks.use",
	"compute.networks.updatePolicy",
	"compute.subnetworks.create",
	"compute.subnetworks.get",
	"compute.subnetworks.use",
	"compute.subnetworks.getIamPolicy",
	"compute.subnetworks.setIamPolicy",
	"compute.globalOperations.get",
	"compute.regionOperations.get",
	"networkconnectivity.serviceConnectionPolicies.create",
	"networkconnectivity.serviceConnectionPolicies.get",
	"networkconnectivity.operations.get",
}
