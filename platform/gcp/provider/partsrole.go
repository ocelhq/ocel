package gcp

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	partsSuffix         = "parts"
	partDatabasesSuffix = "part_databases"
)

var partDatabasesPermissions = []string{
	"datastore.databases.create",
	"datastore.databases.get",
	"datastore.databases.getMetadata",
	"datastore.databases.update",
	"datastore.operations.get",
	"datastore.schemas.create",
	"datastore.schemas.list",
	"datastore.schemas.update",
}

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

func (n Names) PartDatabasesRole() string { return customRoleID(n.namespace, partDatabasesSuffix) }

func (n Names) PartDatabasesRolePath() string {
	return "projects/" + n.project + "/roles/" + n.PartDatabasesRole()
}

func newPartDatabasesRole(names Names) customRole {
	return customRole{
		id:          names.PartDatabasesRole(),
		title:       "ocel bootstrap part databases (" + string(names.namespace) + ")",
		description: "lets a deploy make the Firestore databases a part of a bootstrap holds, and their indexes, granted on the condition it names those databases",
		permissions: partDatabasesPermissions,
		driftReason: "it holds other permissions than a deploy needs to make the databases a part of a bootstrap holds",
	}
}

func partDatabases(names Names) []string {
	var databases []string
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		databases = append(databases, names.TagDatabase(tier), names.TaskDatabase(tier))
	}
	return databases
}

func partDatabasesCondition(names Names) string {
	clauses := make([]string, 0, 2*len(partDatabases(names)))
	for _, database := range partDatabases(names) {
		path := "projects/" + names.project + "/databases/" + database
		clauses = append(clauses, fmt.Sprintf("resource.name == %q", path), fmt.Sprintf("resource.name.startsWith(%q)", path+"/"))
	}
	return strings.Join(clauses, " || ")
}
