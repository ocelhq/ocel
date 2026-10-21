package credential

import (
	"fmt"
	"slices"
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

var projectPermissions = []string{
	"cloudkms.cryptoKeys.create",
	"cloudkms.cryptoKeys.list",
}

const (
	RecordsRole     = "roles/datastore.viewer"
	TaskRecordsRole = "roles/datastore.user"
)

var appGrantedRoles = []string{RecordsRole, TaskRecordsRole}

type Deploy struct {
	names  names.Names
	region string
	tier   environment.Tier
}

func New(names names.Names, region string, tier environment.Tier) Deploy {
	return Deploy{names: names, region: region, tier: tier}
}

func (d Deploy) TierCondition() *policy.Condition {
	return &policy.Condition{
		Title:      "ocel " + string(d.tier) + " tier or untagged",
		Expression: fmt.Sprintf("!resource.hasTagKey('%[1]s') || resource.matchTag('%[1]s', '%[2]s')", d.names.TierTagKeyPath(), d.tier),
	}
}

func (d Deploy) Tiered(role string) Grant {
	return Grant{Role: role, Condition: d.TierCondition()}
}

func (d Deploy) ProjectPermissions() Grant {
	return Grant{
		Permissions: projectPermissions,
		Condition: &policy.Condition{
			Title:      "ocel key ring",
			Expression: fmt.Sprintf("resource.name == %q", d.keyRingPath()),
		},
		Why: "roles/cloudkms.admin on the key ring both tiers share would give away the ring's policy, which every key in it inherits",
	}
}

func (d Deploy) TagValue() Grant {
	return Grant{Role: TagUserRole, On: Resource{Noun: "the tag value", Name: d.names.TierTagValuePath(d.tier)}}
}

func (d Deploy) keyRingPath() string {
	return "projects/" + d.names.Project() + "/locations/" + d.region + "/keyRings/" + d.names.KeyRing()
}

func (d Deploy) projectKeysPrefix() string {
	return d.keyRingPath() + "/cryptoKeys/" + ports.ProjectKeyPrefix(d.tier)
}

func (d Deploy) Sealing() Grant {
	tierKey := d.keyRingPath() + "/cryptoKeys/" + string(d.tier)
	return Grant{Role: SealingRole, Condition: &policy.Condition{
		Title:      "ocel " + string(d.tier) + " keys",
		Expression: fmt.Sprintf("resource.name == %q || resource.name.startsWith(%q)", tierKey, d.projectKeysPrefix()),
	}}
}

func (d Deploy) ProjectKeys() Grant {
	return Grant{Role: ProjectKeysRole, Condition: &policy.Condition{
		Title:      "ocel " + string(d.tier) + " project keys",
		Expression: fmt.Sprintf("resource.name.startsWith(%q)", d.projectKeysPrefix()),
	}}
}

func (d Deploy) Queue() Grant {
	return Grant{Role: QueueRole, On: Resource{Noun: "the queue", Name: d.names.DelayQueuePath(d.region, d.tier)}}
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
		Role: d.names.PartsRolePath(),
		Why:  "Compute Engine, Network Connectivity and Service Usage name no resource attribute an IAM condition could hold a part by, and a tier's network carries no tier tag",
	}
}

func (d Deploy) AppGrants() Grant {
	grantable := append(slices.Clone(appGrantedRoles), d.names.CDNPurgeRolePath())
	quoted := make([]string, len(grantable))
	for i, role := range grantable {
		quoted[i] = "'" + role + "'"
	}
	return Grant{Role: ProjectGrantsRole, Condition: &policy.Condition{
		Title:      "ocel app grants",
		Expression: modifiedGrantsByRole + ".hasOnly([" + strings.Join(quoted, ", ") + "])",
	}}
}

func (d Deploy) Grants() []Grant {
	grants := make([]Grant, 0, len(TieredRoles)+11)
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
	)
}
