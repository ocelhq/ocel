package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	standingSecret    = "postgres://app:hunter2@db.internal/orders"
	standingSensitive = "sk-live-2f8c1a"
	standingPlain     = "eu-west-1"
	standingRecord    = `{"name":"main","postgres":{"host":"db.internal","password":"hunter2"}}`
)

func valuedApp() providerkit.AppPlan {
	app := anApp()
	app.Values = providerkit.AppValues{Delivered: map[string]string{
		"DATABASE_URL":                standingSecret,
		"API_TOKEN":                   standingSensitive,
		"REGION":                      standingPlain,
		"OCEL_RESOURCE_POSTGRES_main": standingRecord,
	}}
	return app
}

func everyValue() []string {
	return []string{standingSecret, standingSensitive, standingPlain, standingRecord}
}

func TestTheValuesAnAppIsHandedReachItThroughAFileAndNoOtherWay(t *testing.T) {
	t.Parallel()

	machine := &box{}
	standing, err := over(machine).ProvisionContainers(context.Background(), aStack(t, valuedApp()), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	path := host.EnvFile(providerkit.ClassProduction, standing[0].Physical)

	machine.mu.Lock()
	defer machine.mu.Unlock()
	file := ""
	for at, command := range machine.ran {
		if strings.Contains(command, "install") && strings.Contains(command, path) {
			file = machine.fed[at]
		}
	}
	if file == "" {
		t.Fatalf("nothing wrote %s:\n%s", path, strings.Join(machine.ran, "\n"))
	}
	for _, value := range everyValue() {
		if !strings.Contains(file, value) {
			t.Errorf("the env file reads %q and does not carry every value the deploy resolved", file)
		}
		for _, command := range machine.ran {
			if strings.Contains(command, value) {
				t.Errorf("a value the deploy resolved is spoken in the command %q, which every login on this box reads out of `ps`", command)
			}
		}
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

func TestAnAppHandedNoValuesIsStoodUpWithNoFileAtAll(t *testing.T) {
	t.Parallel()

	machine := &box{}
	if _, err := over(machine).ProvisionContainers(context.Background(), aStack(t, anApp()), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	joined := strings.Join(machine.commands(), "\n")
	if strings.Contains(joined, "--env-file") {
		t.Errorf("an app declaring no value is handed an env file:\n%s", joined)
	}
	if strings.Contains(joined, "install -m 0600") {
		t.Errorf("an app declaring no value had a file written for it:\n%s", joined)
	}
}
