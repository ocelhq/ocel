package vps_test

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"strings"
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/runtimekit/originguard"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	sensitiveValue = "sk-live-2f8c1a"
	plainValue     = "eu-west-1"
)

func valuedApp() provider.AppSpec {
	app := anApp()
	app.Values = provider.AppValues{
		ContainerEnv: map[string]string{"API_TOKEN": sensitiveValue, "REGION": plainValue},
		Secrets:      []provider.SecretRef{{Key: "DATABASE_URL"}, {Key: "SESSION_SECRET", Folder: "/web"}},
		Bindings:     []provider.Binding{{Name: "main", Type: provider.BindingPostgres, Version: 3}},
	}
	return app
}

func everyValue() []string {
	return []string{sensitiveValue, plainValue}
}

func envFileWritten(t *testing.T, machine *box, class edge.Class, physical string) string {
	t.Helper()
	path := host.EnvFile(class, physical)
	machine.mu.Lock()
	defer machine.mu.Unlock()
	for at, command := range machine.ran {
		if strings.Contains(command, "install") && strings.Contains(command, path) {
			return machine.fed[at]
		}
	}
	t.Fatalf("nothing wrote %s:\n%s", path, strings.Join(machine.ran, "\n"))
	return ""
}

func TestTheValuesAnAppIsHandedReachItThroughAFileAndNoOtherWay(t *testing.T) {
	t.Parallel()

	machine := &box{}
	started, err := over(machine).ProvisionContainers(context.Background(), aStack(t, valuedApp()), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	file := envFileWritten(t, machine, edge.ClassProduction, started[0].Physical)
	for _, value := range everyValue() {
		if !strings.Contains(file, value) {
			t.Errorf("the env file reads %q and does not include every value the deploy delivered", file)
		}
		for _, command := range machine.ran {
			if strings.Contains(command, value) {
				t.Errorf("a value the deploy delivered is spoken in the command %q, which every login on this box reads out of `ps`", command)
			}
		}
	}
}

func TestASecretAndABindingReachTheContainerAsAManifestRatherThanAsValues(t *testing.T) {
	t.Parallel()

	machine := &box{}
	started, err := over(machine).ProvisionContainers(context.Background(), aStack(t, valuedApp()), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	file := envFileWritten(t, machine, edge.ClassProduction, started[0].Physical)
	if strings.Contains(file, "DATABASE_URL=") || strings.Contains(file, "SESSION_SECRET=") || strings.Contains(file, "OCEL_RESOURCE_POSTGRES_main=") {
		t.Errorf("the env file reads %q and contains a secret or a binding record, which the runtime reads live off the box instead", file)
	}
	if !strings.Contains(file, originguard.HealthPathVar+"=/healthz\n") {
		t.Errorf("the env file reads %q and never names the health path the runtime lets the proxy's probe through on", file)
	}
	line := ""
	for entry := range strings.Lines(file) {
		if value, named := strings.CutPrefix(entry, vars.EnvVar+"="); named {
			line = strings.TrimSpace(value)
		}
	}
	manifest, err := vars.Parse([]byte(line))
	if err != nil {
		t.Fatalf("%s = %q, which the runtime cannot read: %v", vars.EnvVar, line, err)
	}
	if manifest.Slug != "shop" || manifest.Class != "production" || manifest.Environment != "" {
		t.Errorf("manifest = %+v, want the project slug and class with no environment in production", manifest)
	}
	if len(manifest.Keys) != 2 || manifest.Keys[0].Key != "DATABASE_URL" || manifest.Keys[1].Key != "SESSION_SECRET" || manifest.Keys[1].Folder != "/web" {
		t.Errorf("manifest pins %+v, want each secret by key and folder", manifest.Keys)
	}
	if len(manifest.Bindings) != 1 || manifest.Bindings[0].Name != "main" ||
		manifest.Bindings[0].Key != provider.ResourceEnvName(provider.BindingPostgres, "main") ||
		manifest.Bindings[0].Type != bindingsv1.BindingType_BINDING_TYPE_POSTGRES || manifest.Bindings[0].Granted != 3 {
		t.Errorf("manifest pins %+v, want the binding by name, the key the app reads it under, its type and the version granted", manifest.Bindings)
	}
	joined := strings.Join(machine.commands(), "\n")
	if !strings.Contains(joined, "src="+vars.SocketDir+",dst="+vars.SocketDir+",readonly") {
		t.Errorf("the container is handed no socket to read its values through:\n%s", joined)
	}
}

func TestAPreviewContainerReadsItsOwnEnvironmentsValues(t *testing.T) {
	t.Parallel()

	machine := &box{}
	spec := aStack(t, valuedApp())
	spec.Ref.Class = edge.ClassPreview
	started, err := over(machine).ProvisionContainers(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	file := envFileWritten(t, machine, edge.ClassPreview, started[0].Physical)
	if !strings.Contains(file, `"class":"preview"`) || !strings.Contains(file, `"environment":"`+spec.Ref.Name.Env+`"`) {
		t.Errorf("the env file reads %q, want a manifest naming the preview class and the stack's environment so its own value shadows the class-wide one", file)
	}
}

func TestNoValueTheDeployResolvedIsInWhatTheDeploySays(t *testing.T) {
	t.Parallel()

	machine := &box{}
	spoken := &said{}
	if _, err := over(machine).ProvisionContainers(context.Background(), aStack(t, valuedApp()), spoken); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	output := strings.Join(spoken.lines, "\n")
	if output == "" {
		t.Fatal("the deploy said nothing at all, so what it does not say proves nothing")
	}
	for _, value := range everyValue() {
		if strings.Contains(output, value) {
			t.Errorf("a value the deploy resolved is in what it said:\n%s", output)
		}
	}
}

func TestAnAppDeclaringNothingIsHandedTheHealthPathAloneAndNoSocket(t *testing.T) {
	t.Parallel()

	machine := &box{}
	started, err := over(machine).ProvisionContainers(context.Background(), aStack(t, anApp()), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if file := envFileWritten(t, machine, edge.ClassProduction, started[0].Physical); file != originguard.HealthPathVar+"=/healthz\n" {
		t.Errorf("an app declaring no value is handed %q, want the health path alone", file)
	}
	joined := strings.Join(machine.commands(), "\n")
	if strings.Contains(joined, "--mount") || strings.Contains(joined, vars.SocketDir) {
		t.Errorf("an app declaring nothing live is handed the box socket anyway:\n%s", joined)
	}
}

func TestAValueDeliveredUnderANameTheRuntimeReadsItsOwnFromIsRefused(t *testing.T) {
	t.Parallel()

	for _, owned := range []string{originguard.HealthPathVar, vars.EnvVar} {
		app := anApp()
		app.Values = provider.AppValues{ContainerEnv: map[string]string{owned: "x"}}
		_, err := over(&box{}).ProvisionContainers(context.Background(), aStack(t, app), nil)
		var rejection refusal.Refusal
		if !errors.As(err, &rejection) || rejection.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), owned) {
			t.Errorf("a value delivered as %s was started: %v", owned, err)
		}
	}
}

func TestTheProviderWrapsEveryContainerInTheRuntimeItShips(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	for arch, machine := range map[string]elf.Machine{host.ArchAMD64: elf.EM_X86_64, host.ArchARM64: elf.EM_AARCH64} {
		raw, err := p.Runtime().Binary(context.Background(), arch)
		if err != nil {
			t.Fatalf("ContainerRuntime(%s) = %v", arch, err)
		}
		binary, err := elf.NewFile(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("ContainerRuntime(%s) is no ELF binary: %v", arch, err)
		}
		if binary.Machine != machine {
			t.Errorf("ContainerRuntime(%s) is built for %s", arch, binary.Machine)
		}
	}
	if _, err := p.Runtime().Binary(context.Background(), "riscv64"); err == nil {
		t.Error("ContainerRuntime(riscv64) handed something back, and a riscv64 image would be wrapped in a runtime that cannot run on it")
	}
}
