package vps_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	livePlainValue      = "eu-west-1"
	liveSensitiveValue  = "sk-live-8c41ff20b7"
	liveSecretValue     = "postgres://app:hunter2@db.internal:5432/orders"
	liveBindingPassword = "opensesame-9f21"
)

func liveScope() envvars.Scope {
	return envvars.Scope{Project: "shop", Class: edge.ClassProduction}
}

func liveStore(p *vps.Provider) envvars.Store {
	return envvars.Store{Records: p.Records(), Cipher: p.Cipher()}
}

type liveValues struct {
	declared provider.AppValues
	reads    map[string]string
}

func resolving(t *testing.T, p *vps.Provider) liveValues {
	t.Helper()
	ctx := context.Background()
	store := liveStore(p)
	if _, err := store.Set(ctx, liveScope(), envvars.Coordinate{Cell: envvars.Cell{Key: "DATABASE_URL"}}, liveSecretValue, nil); err != nil {
		t.Fatalf("sealing a secret through the box's own helper = %v", err)
	}
	pair, err := providerkit.BindingPair("terraform", &bindingsv1.Binding{
		Name:   "main",
		Source: "terraform",
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host: "db.internal", Port: 5432, Database: "orders", Username: "app", Password: liveBindingPassword,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetBinding(ctx, liveScope(), "", "terraform", "main", pair); err != nil {
		t.Fatalf("publishing a binding onto the box = %v", err)
	}

	reader := envvars.EnvironmentReader{Records: p.Records(), Cipher: p.Cipher(), Scope: liveScope()}
	records, err := reader.Bindings(ctx, []string{"main"})
	if err != nil {
		t.Fatalf("resolving a binding back through the helper = %v", err)
	}
	return liveValues{
		declared: provider.AppValues{
			Delivered: map[string]string{"REGION": livePlainValue, "API_TOKEN": liveSensitiveValue},
			Secrets:   []provider.SecretRef{{Key: "DATABASE_URL"}},
			Bindings:  []provider.Binding{{Name: "main", Type: provider.BindingPostgres}},
		},
		reads: map[string]string{
			"REGION":       livePlainValue,
			"API_TOKEN":    liveSensitiveValue,
			"DATABASE_URL": liveSecretValue,
			provider.ResourceEnvName(provider.BindingPostgres, "main"): string(records[0].Value),
		},
	}
}

func liveValuePlan(t *testing.T, tag string, declared provider.AppValues) provider.StackPlan {
	t.Helper()
	plan := livePlan(t, tag)
	plan.App.Values = declared
	return plan
}

func (vm machine) proves(t *testing.T, path string) {
	t.Helper()
	vm.ssh(t, "sudo install -m 600 /dev/null "+quote(path))
	if !vm.stands(t, path) {
		t.Fatalf("this machine reads %s as gone with a file standing at it, so nothing it says about %s being gone after a deploy means anything", path, path)
	}
	vm.ssh(t, "sudo rm -f "+quote(path))
	if vm.stands(t, path) {
		t.Fatalf("this machine reads %s as standing after it was taken, so nothing it says about a path means anything", path)
	}
}

func aPort(said string) bool {
	port, err := strconv.Atoi(said)
	return err == nil && port > 0 && port < 65536
}

func (vm machine) reads(t *testing.T, container, name string) string {
	t.Helper()
	return strings.TrimSpace(vm.beside(t, container, "curl -sS -m 10 'http://127.0.0.1:"+appbuild.InjectedPortText+"/env?name="+name+"'"))
}

func TestLiveAContainerReadsEveryValueClassOffItsOwnEnvironmentAndNothingIsLeftOnTheBox(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	held := resolving(t, p)
	held.declared.Delivered["RELEASE"] = "handed-by-the-deploy"
	spoken := &said{}
	standing, err := p.ProvisionContainers(context.Background(), liveValuePlan(t, "one", held.declared), spoken)
	if err != nil {
		t.Fatalf("ProvisionContainers() with values = %v", err)
	}
	physical := standing[0].Physical

	for name, want := range held.reads {
		if got := vm.reads(t, physical, name); got != want {
			t.Errorf("the app reads %s as %q off its own environment, want %q", name, got, want)
		}
	}
	if got := vm.reads(t, physical, "RELEASE"); got != "handed-by-the-deploy" {
		t.Errorf("the app reads RELEASE as %q: the image sets it in its own `ENV` line, and what the deploy hands a container outranks an image's defaults, deliberately", got)
	}
	if got := vm.reads(t, physical, "PORT"); got == appbuild.InjectedPortText || !aPort(got) {
		t.Errorf("the app reads PORT as %q: the runtime answers on %s and fronts the app on a loopback port of its own choosing, which outranks anything an env file names", got, appbuild.InjectedPortText)
	}

	path := host.EnvFile(edge.ClassProduction, physical)
	vm.proves(t, path)
	if vm.stands(t, path) {
		t.Errorf("%s survived the deploy that wrote it, and it holds every value the deploy resolved in plaintext", path)
	}

	output := strings.Join(spoken.lines, "\n")
	if output == "" {
		t.Fatal("the deploy said nothing at all, so what it does not say proves nothing")
	}
	for name, value := range held.reads {
		if strings.Contains(output, value) {
			t.Errorf("%s's value is in what this deploy said:\n%s", name, output)
		}
	}

	inspected := vm.inspects(t, "container", physical, "{{json .Config.Env}}")
	for _, value := range []string{liveSecretValue, liveBindingPassword} {
		if strings.Contains(inspected, value) {
			t.Errorf("the container's configuration reads %q and carries a value the runtime reads live: anyone who may inspect the container would read it", inspected)
		}
	}
	if !strings.Contains(inspected, liveSensitiveValue) {
		t.Errorf("the container's configuration reads %q and does not carry the sensitive value the deploy baked in", inspected)
	}

	rotated := liveSecretValue + "-rotated"
	if _, err := liveStore(p).Set(context.Background(), liveScope(), envvars.Coordinate{Cell: envvars.Cell{Key: "DATABASE_URL"}}, rotated, nil); err != nil {
		t.Fatalf("rotating the secret after the deploy = %v", err)
	}
	deadline := time.Now().Add(3 * live.StalenessBound)
	for {
		if got := vm.reads(t, physical, "DATABASE_URL"); got == rotated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the app still reads the old DATABASE_URL %s after the secret was rotated on the box, and a live value lands without a deploy", 3*live.StalenessBound)
		}
		time.Sleep(5 * time.Second)
	}
}

func TestLiveTheEnvFileStandsAtSixHundredForTheDeployLoginForAsLongAsItExists(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	plan := liveValuePlan(t, "two", resolving(t, p).declared)
	physical := host.ContainerName(plan.Ref.Name.String(), plan.App.App, plan.App.Deployment, plan.App.Image)
	path := host.EnvFile(edge.ClassProduction, physical)

	watching := "until=$(( $(date +%s) + 180 ))\n" +
		"while [ \"$(date +%s)\" -lt \"$until\" ]; do\n" +
		"if [ -e " + quote(path) + " ]; then stat -c '%a %U' " + quote(path) + "; exit 0; fi\n" +
		"sleep 0.05\n" +
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
			t.Fatalf("%s was never sampled while it stood: the deploy writes it, runs a container off it and takes it back over three round trips to this machine, so a watcher that missed all three read a path this deploy never wrote and every other reading of that path in this suite proves nothing",
				path)
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

	held := resolving(t, p)
	broken, err := p.ProvisionContainers(context.Background(), liveValuePlan(t, "crasher", held.declared), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() of a crash-looping app = %v, want it stood up and refused at its gate", err)
	}
	physical := broken[0].Physical

	refusal := releasing(p, release{physical: physical, address: physical + ":" + appbuild.InjectedPortText}, 5*time.Second, nil)
	if refusal == nil {
		t.Fatal("a release of the crash-looping fixture passed its gate")
	}
	said := refusal.Error()

	for _, want := range []string{"Status=", "RestartCount=", "logs (last"} {
		if !strings.Contains(said, want) {
			t.Errorf("the evidence a failed release captured reads\n%s\nand never names %s", said, want)
		}
	}
	for name, value := range held.reads {
		if strings.Contains(said, value) {
			t.Errorf("%s's value is in the evidence a failed release captured:\n%s", name, said)
		}
	}
	if strings.Contains(said, "API_TOKEN=") || strings.Contains(said, "DATABASE_URL=") {
		t.Errorf("the evidence a failed release captured names the container's environment:\n%s", said)
	}

	path := host.EnvFile(edge.ClassProduction, physical)
	vm.proves(t, path)
	if vm.stands(t, path) {
		t.Errorf("%s survived a deploy that fell over", path)
	}
}

func TestLiveAContainerThatCannotBeStoodUpTakesItsEnvFileWithIt(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	held := resolving(t, p)
	plan := liveValuePlan(t, "one", held.declared)
	plan.App.Image = fixtureRepo + ":no-such-tag"
	physical := host.ContainerName(plan.Ref.Name.String(), plan.App.App, plan.App.Deployment, plan.App.Image)

	_, err := p.ProvisionContainers(context.Background(), plan, nil)
	if err == nil {
		t.Fatal("ProvisionContainers() over an image this box does not hold succeeded")
	}

	path := host.EnvFile(edge.ClassProduction, physical)
	vm.proves(t, path)
	if vm.stands(t, path) {
		t.Errorf("%s survived a stand-up that never happened, and nothing after this deploy takes it back", path)
	}
	for name, value := range held.reads {
		if strings.Contains(err.Error(), value) {
			t.Errorf("%s's value is in the refusal a failed stand-up returned: %s", name, err)
		}
	}
}
