package gcp

import (
	"slices"
)

const partsSuffix = "parts"

const servicesReadPermission = "serviceusage.services.get"

func (n Names) PartsRole() string { return customRoleID(n.namespace, partsSuffix) }

func (n Names) PartsRolePath() string {
	return "projects/" + n.project + "/roles/" + n.PartsRole()
}

func newPartsRole(names Names) customRole {
	return customRole{
		id:          names.PartsRole(),
		title:       "ocel bootstrap parts (" + string(names.namespace) + ")",
		description: "lets a deploy add the parts of a bootstrap it needs on first use, where Google offers no condition to name them by, and nothing else",
		permissions: slices.Concat([]string{servicesReadPermission}, networkPartPermissions),
		driftReason: "it holds other permissions than a deploy needs to add the parts of a bootstrap",
	}
}
