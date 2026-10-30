package deploy

import (
	"testing"

	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
)

func TestEveryListedPropertyIsAnArgumentOfItsTerraformResource(t *testing.T) {
	t.Parallel()

	componenttest.RefuseInventedArguments(t, items(t, nil, inventoryRequest(t)))
}

func referencedBy(t *testing.T, set *costv1.ResourceSet, typ, name, path string) *costv1.Resource {
	t.Helper()
	for _, from := range set.GetResources() {
		if from.GetType() != typ || from.GetName() != name {
			continue
		}
		id, ok := from.GetReferences()[path]
		if !ok {
			t.Fatalf("%s references %v, want %s", from.GetId(), from.GetReferences(), path)
		}
		for _, to := range set.GetResources() {
			if to.GetId() == id {
				return to
			}
		}
		t.Fatalf("%s's %s names %s, which the set does not hold", from.GetId(), path, id)
	}
	t.Fatalf("no %s named %s", typ, name)
	return nil
}

func TestAnAuroraInstanceReferencesTheClusterThatHoldsItsServerlessScaling(t *testing.T) {
	t.Parallel()

	set := items(t, nil, inventoryRequest(t))

	cluster := referencedBy(t, set, "aws_rds_cluster_instance", "main", "cluster_identifier")
	if cluster.GetType() != "aws_rds_cluster" || cluster.GetName() != "main" {
		t.Errorf("the instance references %s, want the main cluster", cluster.GetId())
	}
	if _, listed := listedNamed(t, set, "aws_rds_cluster_instance", "main")["serverlessv2_scaling_configuration"]; listed {
		t.Error("the instance lists serverless scaling, which Terraform holds on the cluster")
	}
}

func TestAFargateServiceReferencesTheTaskDefinitionThatSizesIt(t *testing.T) {
	t.Parallel()

	set := items(t, nil, inventoryRequest(t))

	for _, name := range []string{"api", resources.WorkerName("media")} {
		definition := referencedBy(t, set, "aws_ecs_service", name, "task_definition")
		if definition.GetType() != "aws_ecs_task_definition" || definition.GetName() != name {
			t.Errorf("the %s service references %s, want its own task definition", name, definition.GetId())
		}
		if definition.GetScope() != listedScope(t, set, "aws_ecs_service", name) {
			t.Errorf("the %s task definition sits in %s, want its service's scope", name, definition.GetScope())
		}
	}
}

func listedScope(t *testing.T, set *costv1.ResourceSet, typ, name string) string {
	t.Helper()
	for _, r := range set.GetResources() {
		if r.GetType() == typ && r.GetName() == name {
			return r.GetScope()
		}
	}
	t.Fatalf("no %s named %s", typ, name)
	return ""
}
