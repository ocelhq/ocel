package grants

import (
	"regexp"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

var basicRoles = []string{"roles/owner", "roles/editor", "roles/viewer"}

var roleName = regexp.MustCompile(`^(roles|(projects|organizations)/[^/]+/roles)/[A-Za-z0-9_.]+$`)

const resourceKinds = "a project (projects/<id>), a service account (projects/<project>/serviceAccounts/<email>), " +
	"a bucket (projects/_/buckets/<name>), an Artifact Registry repository (projects/<project>/locations/<location>/repositories/<name>) " +
	"or a Pub/Sub topic (projects/<project>/topics/<name>)"

var resourceForms = []struct {
	pattern *regexp.Regexp
	target  func(project string, match []string) grantTarget
}{
	{regexp.MustCompile(`^projects/([a-z][a-z0-9-]{4,28}[a-z0-9])$`), func(project string, match []string) grantTarget {
		if match[1] == project {
			return grantTarget{Kind: Project}
		}
		return grantTarget{Kind: Project, Name: match[1]}
	}},
	{regexp.MustCompile(`^projects/[^/]+/serviceAccounts/[^/@]+@[^/]+$`), func(_ string, match []string) grantTarget {
		return grantTarget{Kind: ServiceAccount, Name: match[0]}
	}},
	{regexp.MustCompile(`^projects/_/buckets/([a-z0-9][a-z0-9._-]{1,220}[a-z0-9])$`), func(_ string, match []string) grantTarget {
		return grantTarget{Kind: Bucket, Name: match[1]}
	}},
	{regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/repositories/[^/]+$`), func(_ string, match []string) grantTarget {
		return grantTarget{Kind: Repository, Name: match[0]}
	}},
	{regexp.MustCompile(`^projects/[^/]+/topics/[^/]+$`), func(_ string, match []string) grantTarget {
		return grantTarget{Kind: Topic, Name: match[0]}
	}},
}

func OnBindings(project string, bound []provider.Binding, member string) ([]Grant, error) {
	var granted []Grant
	for _, binding := range bound {
		for _, grant := range binding.Grants {
			target, err := bindingTarget(project, binding.Name, grant)
			if err != nil {
				return nil, err
			}
			granted = append(granted, Grant{Target: target, Role: grant.Role, Member: member, Binding: binding.Name})
		}
	}
	return granted, nil
}

func RefuseUnusable(apps []provider.AppUsage) error {
	for _, app := range apps {
		if _, err := OnBindings("", app.Grants, ""); err != nil {
			return err
		}
	}
	return nil
}

func bindingTarget(project, binding string, grant provider.Grant) (grantTarget, error) {
	switch {
	case grant.Role == "":
		return grantTarget{}, refusal.Refuse(refusal.CodeInvalid,
			"binding %s grants %s over %s, and Google Cloud grants an app a role on a resource: publish the grant as a role and the resource it is held on",
			binding, strings.Join(grant.Actions, ", "), strings.Join(grant.Resources, ", "))
	case slices.Contains(basicRoles, grant.Role):
		return grantTarget{}, refusal.Refuse(refusal.CodeInvalid,
			"binding %s grants %s on %s, a basic role that reaches every service of the resource: grant the predefined or custom role the app needs instead",
			binding, grant.Role, grant.Resource)
	case !roleName.MatchString(grant.Role):
		return grantTarget{}, refusal.Refuse(refusal.CodeInvalid,
			"binding %s grants %q on %s, which is not a role name: name it roles/<role>, or projects/<project>/roles/<role> for a custom role",
			binding, grant.Role, grant.Resource)
	case len(grant.Conditions) > 0:
		return grantTarget{}, refusal.Refuse(refusal.CodeInvalid,
			"binding %s grants %s on %s under a condition, which ocel does not set on a Google Cloud role: publish the grant without it, on the narrowest resource that holds what the app needs",
			binding, grant.Role, grant.Resource)
	}
	for _, form := range resourceForms {
		if match := form.pattern.FindStringSubmatch(grant.Resource); match != nil {
			return form.target(project, match), nil
		}
	}
	return grantTarget{}, refusal.Refuse(refusal.CodeInvalid,
		"binding %s grants %s on %q, which names no resource ocel grants a role on: name %s",
		binding, grant.Role, grant.Resource, resourceKinds)
}

var policyPermissions = map[targetKind]string{
	Project:        "resourcemanager.projects.setIamPolicy",
	ServiceAccount: "iam.serviceAccounts.setIamPolicy",
	Repository:     "artifactregistry.repositories.setIamPolicy",
	Bucket:         "storage.buckets.setIamPolicy",
	Topic:          "pubsub.topics.setIamPolicy",
}

func explainRefusedBindingGrant(c *gcloud.Clients, tier environment.Tier, grant Grant, err error) error {
	if !gcloud.Forbidden(err) {
		return err
	}
	resource := grant.Target.Name
	switch {
	case grant.Target.Kind == Project && resource == "":
		resource = "projects/" + c.Project()
	case grant.Target.Kind == Project:
		resource = "projects/" + resource
	case grant.Target.Kind == Bucket:
		resource = "projects/_/buckets/" + resource
	}
	return refusal.Refuse(refusal.CodeDenied,
		"binding %s grants %s on %s, and the %s deploy credential may not change who holds roles there: give it %s on %s, then deploy again",
		grant.Binding, grant.Role, resource, tier, policyPermissions[grant.Target.Kind], resource)
}
