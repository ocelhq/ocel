package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func servingAs(t *testing.T, box *bench, served ResourceContainer, status string) {
	t.Helper()
	limited, err := box.host().limited(context.Background(), served)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := limited.digest()
	if err != nil {
		t.Fatal(err)
	}
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, shell.Quote(servingSelectors())) {
			return ssh.Result{Stdout: status + " " + served.Image + " " + digest + "\n"}, true
		}
		return ssh.Result{}, false
	}
}

func refuseResourceWrites(t *testing.T, box *bench) {
	t.Helper()
	for _, command := range box.commands() {
		for _, writes := range []string{"docker run", "'docker' 'run'", "docker rm", "docker rename", "docker stop", "docker volume create", "docker network create", "install "} {
			if strings.Contains(command, writes) {
				t.Errorf("planning ran %q, and a plan changes nothing on the box:\n%s", writes, command)
			}
		}
	}
}

func TestAResourceServingTheSpecItWasRunWithPlansKeep(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	servingAs(t, box, resourced(), "running")
	action, reason, err := box.host().PlanResource(context.Background(), resourced())
	if err != nil {
		t.Fatalf("PlanResource() = %v", err)
	}
	if action != provider.ActionKeep || reason != provider.ReasonCurrent {
		t.Errorf("PlanResource() = %s %q, want keep: the container serves exactly this spec", action, reason)
	}
	refuseResourceWrites(t, box)
}

func TestAMissingResourcePlansACreate(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	action, _, err := box.host().PlanResource(context.Background(), resourced())
	if err != nil {
		t.Fatalf("PlanResource() = %v", err)
	}
	if action != provider.ActionCreate {
		t.Errorf("PlanResource() = %s, want create: no container serves it", action)
	}
	refuseResourceWrites(t, box)
}

func TestAResourceWhoseImageChangesPlansAReplaceNamingBothImages(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	older := resourced()
	older.Image = "public.ecr.aws/docker/library/postgres:16.10@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	servingAs(t, box, older, "running")
	action, reason, err := box.host().PlanResource(context.Background(), resourced())
	if err != nil {
		t.Fatalf("PlanResource() = %v", err)
	}
	if action != provider.ActionReplace {
		t.Errorf("PlanResource() = %s, want replace: RunResource swaps the container for one on the new image", action)
	}
	for _, image := range []string{older.Image, resourced().Image} {
		if !strings.Contains(reason, image) {
			t.Errorf("the reason %q does not name %s", reason, image)
		}
	}
	refuseResourceWrites(t, box)
}

func TestAResourceWhoseSettingsChangePlansAReplace(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	servingAs(t, box, resourced(), "running")
	changed := resourced()
	changed.Env = map[string]string{"POSTGRES_DB": "orders"}
	action, reason, err := box.host().PlanResource(context.Background(), changed)
	if err != nil {
		t.Fatalf("PlanResource() = %v", err)
	}
	if action != provider.ActionReplace || !strings.Contains(reason, "settings") {
		t.Errorf("PlanResource() = %s %q, want a replace saying its settings change", action, reason)
	}
	refuseResourceWrites(t, box)
}

func TestAStoppedResourcePlansAReplaceSayingItIsStartedAnew(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	servingAs(t, box, resourced(), "exited")
	action, reason, err := box.host().PlanResource(context.Background(), resourced())
	if err != nil {
		t.Fatalf("PlanResource() = %v", err)
	}
	if action != provider.ActionReplace || !strings.Contains(reason, "exited") {
		t.Errorf("PlanResource() = %s %q, want a replace saying the container exited", action, reason)
	}
}

func TestAResourceOnDataOfAnotherVersionPlansAReplaceNamingBothVersions(t *testing.T) {
	t.Parallel()

	spec := resourced()
	spec.Volume.Generation = "17"
	box := machine(nil)
	box.answer = func(command string) (ssh.Result, bool) {
		switch {
		case strings.Contains(command, shell.Quote(servingSelectors())):
			return ssh.Result{Stdout: "running " + spec.Image + " 000000000000\n"}, true
		case strings.Contains(command, LabelGeneration):
			return ssh.Result{Stdout: "16\n"}, true
		}
		return ssh.Result{}, false
	}
	action, reason, err := box.host().PlanResource(context.Background(), spec)
	if err != nil {
		t.Fatalf("PlanResource() = %v", err)
	}
	if action != provider.ActionReplace || !strings.Contains(reason, "16") || !strings.Contains(reason, "17") {
		t.Errorf("PlanResource() = %s %q, want a replace moving the data from version 16 to 17", action, reason)
	}
	refuseResourceWrites(t, box)
}

func TestPlanningAnUpgradeOverAStoppedServerIsRefusedAsRunningItWouldBe(t *testing.T) {
	t.Parallel()

	spec := resourced()
	spec.Volume.Generation = "17"
	box := machine(nil)
	box.answer = func(command string) (ssh.Result, bool) {
		switch {
		case strings.Contains(command, shell.Quote(servingSelectors())):
			return ssh.Result{Stdout: "exited " + spec.Image + "\n"}, true
		case strings.Contains(command, LabelGeneration):
			return ssh.Result{Stdout: "16\n"}, true
		}
		return ssh.Result{}, false
	}
	_, _, err := box.host().PlanResource(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "Run `docker start "+spec.Name+"`") {
		t.Errorf("PlanResource() = %v, want the refusal RunResource gives over a stopped server with older data", err)
	}
}
