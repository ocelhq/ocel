package vps_test

import (
	"context"
	"strings"
	"testing"
	"time"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	livePlainValue     = "eu-west-1"
	liveSensitiveValue = "sk-live-8c41ff20b7"
	liveSecretValue    = "postgres://app:hunter2@db.internal:5432/orders"
	liveLinkPassword   = "opensesame-9f21"
)

func liveScope() values.Scope {
	return values.Scope{Project: "shop", Class: providerkit.ClassProduction}
}

func liveStore(p *vps.Provider) values.Store {
	return values.Store{Records: p.Records(), Sealer: p.Sealer()}
}

func resolving(t *testing.T, p *vps.Provider) map[string]string {
	t.Helper()
	ctx := context.Background()
	store := liveStore(p)
	if _, err := store.Set(ctx, liveScope(), values.Coordinate{Cell: values.Cell{Key: "DATABASE_URL"}}, liveSecretValue, nil); err != nil {
		t.Fatalf("sealing a secret through the box's own helper = %v", err)
	}
	pair, err := providerkit.LinkPair("terraform", &linksv1.Link{
		Name:   "main",
		Source: "terraform",
		Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{
			Host: "db.internal", Port: 5432, Database: "orders", Username: "app", Password: liveLinkPassword,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetLink(ctx, liveScope(), "", "terraform", "main", pair); err != nil {
		t.Fatalf("publishing a link onto the box = %v", err)
	}

	reader := values.Reader{Records: p.Records(), Sealer: p.Sealer(), Scope: liveScope()}
	opened, err := reader.Values(ctx, []values.Cell{{Key: "DATABASE_URL"}})
	if err != nil {
		t.Fatalf("opening a secret back through the helper = %v", err)
	}
	records, err := reader.Links(ctx, []string{"main"})
	if err != nil {
		t.Fatalf("resolving a link back through the helper = %v", err)
	}
	if opened["DATABASE_URL"] != liveSecretValue {
		t.Fatalf("the helper opened %q, and the deploy hands a container what it resolved", opened["DATABASE_URL"])
	}
	return map[string]string{
		"REGION":       livePlainValue,
		"API_TOKEN":    liveSensitiveValue,
		"DATABASE_URL": opened["DATABASE_URL"],
		providerkit.ResourceEnvName(providerkit.LinkPostgres, "main"): string(records[0].Value),
	}
}

func liveValuePlan(t *testing.T, tag string, delivered map[string]string) providerkit.StackPlan {
	t.Helper()
	plan := livePlan(t, tag)
	plan.App.Values = providerkit.AppValues{Delivered: delivered}
	return plan
}

func (vm machine) reads(t *testing.T, container, name string) string {
	t.Helper()
	return strings.TrimSpace(vm.peers(t, "curl -sS -m 10 'http://"+container+":"+host.AppPort+"/env?name="+name+"'"))
}

func TestLiveAContainerReadsEveryValueClassOffItsOwnEnvironmentAndNothingIsLeftOnTheBox(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	delivered := resolving(t, p)
	delivered["RELEASE"] = "handed-by-the-deploy"
	spoken := &said{}
	standing, err := p.ProvisionContainers(context.Background(), liveValuePlan(t, "one", delivered), spoken)
	if err != nil {
		t.Fatalf("ProvisionContainers() with values = %v", err)
	}
	physical := standing[0].Physical

	for name, want := range delivered {
		if got := vm.reads(t, physical, name); got != want {
			t.Errorf("the app reads %s as %q off its own environment, want %q", name, got, want)
		}
	}
	if got := vm.reads(t, physical, "RELEASE"); got != "handed-by-the-deploy" {
		t.Errorf("the app reads RELEASE as %q: the image sets it in its own `ENV` line, and what the deploy hands a container outranks an image's defaults, deliberately", got)
	}
	if got := vm.reads(t, physical, "PORT"); got != host.AppPort {
		t.Errorf("the app reads PORT as %q, and the port the provider injects outranks anything an env file names", got)
	}

	path := host.EnvFile(providerkit.ClassProduction, physical)
	if vm.stands(t, path) {
		t.Errorf("%s survived the deploy that wrote it, and it holds every value the deploy resolved in plaintext", path)
	}

	output := strings.Join(spoken.lines, "\n")
	if output == "" {
		t.Fatal("the deploy said nothing at all, so what it does not say proves nothing")
	}
	for name, value := range delivered {
		if strings.Contains(output, value) {
			t.Errorf("%s's value is in what this deploy said:\n%s", name, output)
		}
	}

	held := vm.inspects(t, "container", physical, "{{json .Config.Env}}")
	if !strings.Contains(held, liveSecretValue) {
		t.Errorf("the container's configuration reads %q and does not carry the secret: an env file hides nothing from an inspect, and a test that says it does would be recording a promise ocel never made", held)
	}
}

func TestLiveTheEnvFileStandsAtSixHundredForTheDeployLoginForAsLongAsItExists(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	delivered := resolving(t, p)
	plan := liveValuePlan(t, "two", delivered)
	physical := host.ContainerName(plan.Ref.Name.String(), plan.App.App, plan.App.Deployment, plan.App.Image)
	path := host.EnvFile(providerkit.ClassProduction, physical)

	watching := "until=$(( $(date +%s) + 180 ))\n" +
		"while [ \"$(date +%s)\" -lt \"$until\" ]; do\n" +
		"if [ -e " + quote(path) + " ]; then stat -c '%a %U' " + quote(path) + "; exit 0; fi\n" +
		"done\n" +
		"echo missed"
	watched := make(chan string, 1)
	go func() {
		watched <- strings.TrimSpace(vm.ssh(t, "sudo sh -c "+quote(watching)))
	}()

	if _, err := p.ProvisionContainers(context.Background(), plan, nil); err != nil {
		t.Fatalf("ProvisionContainers() with values = %v", err)
	}

	select {
	case posture := <-watched:
		if posture == "missed" {
			t.Skip("the env file was written and taken back inside one pass of the watcher, so its posture was never sampled")
		}
		if posture != "600 "+deployLogin {
			t.Errorf("%s stood at %q while it existed, want `600 %s`: every value the app holds is readable by whoever the mode and the owner admit",
				path, posture, deployLogin)
		}
	case <-time.After(5 * time.Minute):
		t.Fatal("the watcher never returned")
	}
}

func TestLiveAReleaseThatFallsOverKeepsNoEnvFileAndSaysNothingOfWhatWasInIt(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	delivered := resolving(t, p)
	broken, err := p.ProvisionContainers(context.Background(), liveValuePlan(t, "crasher", delivered), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() of a crash-looping app = %v, want it stood up and refused at its gate", err)
	}
	physical := broken[0].Physical

	refusal := releasing(p, release{physical: physical, address: physical + ":" + host.AppPort}, "", 5*time.Second, nil)
	if refusal == nil {
		t.Fatal("a release of the crash-looping fixture passed its gate")
	}
	said := refusal.Error()

	for _, want := range []string{"Status=", "RestartCount=", "logs (last"} {
		if !strings.Contains(said, want) {
			t.Errorf("the evidence a failed release captured reads\n%s\nand never names %s", said, want)
		}
	}
	for name, value := range delivered {
		if strings.Contains(said, value) {
			t.Errorf("%s's value is in the evidence a failed release captured:\n%s", name, said)
		}
	}
	if strings.Contains(said, "API_TOKEN=") || strings.Contains(said, "DATABASE_URL=") {
		t.Errorf("the evidence a failed release captured names the container's environment:\n%s", said)
	}

	path := host.EnvFile(providerkit.ClassProduction, physical)
	if vm.stands(t, path) {
		t.Errorf("%s survived a deploy that fell over", path)
	}
}

func TestLiveAContainerThatCannotBeStoodUpTakesItsEnvFileWithIt(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	delivered := resolving(t, p)
	plan := liveValuePlan(t, "one", delivered)
	plan.App.Image = fixtureRepo + ":no-such-tag"
	physical := host.ContainerName(plan.Ref.Name.String(), plan.App.App, plan.App.Deployment, plan.App.Image)

	_, err := p.ProvisionContainers(context.Background(), plan, nil)
	if err == nil {
		t.Fatal("ProvisionContainers() over an image this box does not hold succeeded")
	}

	path := host.EnvFile(providerkit.ClassProduction, physical)
	if vm.stands(t, path) {
		t.Errorf("%s survived a stand-up that never happened, and nothing after this deploy takes it back", path)
	}
	for name, value := range delivered {
		if strings.Contains(err.Error(), value) {
			t.Errorf("%s's value is in the refusal a failed stand-up returned: %s", name, err)
		}
	}
}
