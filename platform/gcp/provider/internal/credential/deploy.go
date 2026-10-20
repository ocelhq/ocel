package credential

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	RunRole              = "roles/run.admin"
	TopicsRole           = "roles/pubsub.admin"
	BucketsRole          = "roles/storage.admin"
	SecretsRole          = "roles/secretmanager.admin"
	RepositoriesRole     = "roles/artifactregistry.admin"
	DatabasesRole        = "roles/datastore.owner"
	InstancesRole        = "roles/cloudsql.admin"
	AccountsRole         = "roles/iam.serviceAccountAdmin"
	RunAsRole            = "roles/iam.serviceAccountUser"
	SealingRole          = "roles/cloudkms.cryptoKeyEncrypterDecrypter"
	ProjectKeysRole      = "roles/cloudkms.admin"
	StoresRole           = "roles/memorystore.admin"
	SchedulesRole        = "roles/cloudscheduler.admin"
	LogsRole             = "roles/logging.viewer"
	QueueRole            = "roles/cloudtasks.queueAdmin"
	TagUserRole          = "roles/resourcemanager.tagUser"
	ProjectGrantsRole    = "roles/resourcemanager.projectIamAdmin"
	modifiedGrantsByRole = "api.getAttribute('iam.googleapis.com/modifiedGrantsByRole', [])"
)

var TieredRoles = []string{RunRole, TopicsRole, BucketsRole, SecretsRole, RepositoriesRole, DatabasesRole, InstancesRole, AccountsRole, RunAsRole}

var UntaggedKinds = []string{
	"run.services",
	"iam.serviceAccounts",
	"artifactregistry.repositories",
	"storage.buckets",
	"cloudsql.instances",
	"pubsub.topics",
	"pubsub.subscriptions",
	"secretmanager.secrets",
	"datastore.databases",
}

var projectPermissions = []string{
	"resourcemanager.projects.get",
	"run.services.create",
	"run.services.list",
	"run.operations.get",
	"run.locations.list",
	"iam.serviceAccounts.create",
	"iam.serviceAccounts.list",
	"pubsub.topics.create",
	"pubsub.subscriptions.create",
	"storage.buckets.create",
	"secretmanager.secrets.create",
	"artifactregistry.repositories.create",
	"datastore.databases.create",
	"datastore.databases.list",
	"datastore.locations.list",
	"cloudsql.instances.create",
	"cloudsql.instances.list",
	"cloudsql.instances.get",
	"cloudkms.cryptoKeys.create",
	"cloudkms.cryptoKeys.list",
}

type Deploy struct {
	Names          names.Names
	Region         string
	Tier           environment.Tier
	GrantableRoles []string
}

func (d Deploy) TierCondition() *policy.Condition {
	return &policy.Condition{
		Title:      "ocel " + string(d.Tier) + " tier",
		Expression: fmt.Sprintf("resource.matchTag('%s', '%s')", d.Names.TierTagKeyPath(), d.Tier),
	}
}

func (d Deploy) UntaggedCondition() *policy.Condition {
	return &policy.Condition{
		Title:      "ocel untagged",
		Expression: fmt.Sprintf("!resource.hasTagKey('%s')", d.Names.TierTagKeyPath()),
	}
}

func (d Deploy) Tiered(role string) Grant {
	return Grant{Role: role, Condition: d.TierCondition()}
}

func (d Deploy) ProjectPermissions() Grant {
	return Grant{
		Permissions: projectPermissions,
		Why:         "Google checks creating a resource, listing a kind and reading the project against the project, which carries no tier tag",
	}
}

func (d Deploy) Untagged() Grant {
	var permissions []string
	for _, kind := range UntaggedKinds {
		permissions = append(permissions, kind+".get", kind+".createTagBinding", kind+".listTagBindings")
	}
	return Grant{
		Permissions: permissions,
		Condition:   d.UntaggedCondition(),
		Why:         "a resource a deploy has just made carries no tier tag until the deploy binds its tier's value, and this reads it and binds it and does nothing else",
	}
}

func (d Deploy) TagValue() Grant {
	return Grant{Role: TagUserRole, On: d.Names.TierTagValuePath(d.Tier)}
}

func (d Deploy) keyRingPath() string {
	return "projects/" + d.Names.Project() + "/locations/" + d.Region + "/keyRings/" + d.Names.KeyRing()
}

func (d Deploy) projectKeysPrefix() string {
	return d.keyRingPath() + "/cryptoKeys/" + ports.ProjectKeyPrefix(d.Tier)
}

func (d Deploy) Sealing() Grant {
	tierKey := d.keyRingPath() + "/cryptoKeys/" + string(d.Tier)
	return Grant{Role: SealingRole, Condition: &policy.Condition{
		Title:      "ocel " + string(d.Tier) + " keys",
		Expression: fmt.Sprintf("resource.name == %q || resource.name.startsWith(%q)", tierKey, d.projectKeysPrefix()),
	}}
}

func (d Deploy) ProjectKeys() Grant {
	return Grant{Role: ProjectKeysRole, Condition: &policy.Condition{
		Title:      "ocel " + string(d.Tier) + " project keys",
		Expression: fmt.Sprintf("resource.name.startsWith(%q)", d.projectKeysPrefix()),
	}}
}

func (d Deploy) Queue() Grant {
	return Grant{Role: QueueRole, On: d.Names.DelayQueuePath(d.Region, d.Tier)}
}

func (d Deploy) Stores() Grant {
	return Grant{Role: StoresRole, Why: "Memorystore instances take no tag, so no condition holds one tier's stores apart"}
}

func (d Deploy) Schedules() Grant {
	return Grant{Role: SchedulesRole, Why: "Cloud Scheduler jobs take no tag, so no condition holds one tier's jobs apart"}
}

func (d Deploy) Logs() Grant {
	return Grant{Role: LogsRole, Why: "no log view selects one tier's entries: a Cloud Run entry names its service and revision and no tier"}
}

func (d Deploy) Parts() Grant {
	return Grant{
		Role: d.Names.PartsRolePath(),
		Why:  "Compute Engine, Network Connectivity and Service Usage name no resource attribute an IAM condition could hold a part by, and a tier's network carries no tier tag",
	}
}

func (d Deploy) AppGrants() Grant {
	quoted := make([]string, len(d.GrantableRoles))
	for i, role := range d.GrantableRoles {
		quoted[i] = "'" + role + "'"
	}
	return Grant{Role: ProjectGrantsRole, Condition: &policy.Condition{
		Title:      "ocel app grants",
		Expression: modifiedGrantsByRole + ".hasOnly([" + strings.Join(quoted, ", ") + "])",
	}}
}

func (d Deploy) Grants() []Grant {
	grants := make([]Grant, 0, len(TieredRoles)+12)
	for _, role := range TieredRoles {
		grants = append(grants, d.Tiered(role))
	}
	return append(grants,
		d.Sealing(),
		d.ProjectKeys(),
		d.Stores(),
		d.Schedules(),
		d.Logs(),
		d.Queue(),
		d.TagValue(),
		d.AppGrants(),
		d.Parts(),
		d.ProjectPermissions(),
		d.Untagged(),
	)
}
