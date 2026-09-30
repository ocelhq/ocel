package vps_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func newBoxServingPreviews(t *testing.T, base string) *scripted {
	t.Helper()
	table, err := host.WriteRoutingTable(host.RoutingTable{Grace: 30 * time.Second, PreviewBase: base})
	if err != nil {
		t.Fatal(err)
	}
	return boxSaying(map[string]answer{"flock -s 9": {stdout: "+" + base64.StdEncoding.EncodeToString(table) + "\n\n"}})
}

func preflightTier(machine *scripted, options vps.Options, tier environment.Tier) error {
	options.SSH = vps.Target{Host: "box.invalid", User: "ada"}
	p := vps.ProviderOver(options, func(context.Context) (host.Conn, error) { return machine, nil })
	stack, err := naming.ParseStackName("pr-7--web--r0a1b2c3d")
	if err != nil {
		return err
	}
	return p.PreflightDeploy(context.Background(), provider.DeployPreflight{
		Deploy: provider.DeploySpec{
			Slug: "shop",
			Tier: tier,
			Env:  "pr-7",
			Apps: []provider.AppEntry{{App: "web", Stack: stack, Image: deployedRef}},
		},
	})
}

func TestAPreviewDeployWhoseHostnamesWouldEachBeCertifiedPubliclyIsRefusedBeforeAnyIsClaimed(t *testing.T) {
	t.Parallel()

	machine := newBoxServingPreviews(t, "preview.example.com")
	err := preflightTier(machine, vps.Options{}, environment.TierPreview)
	if err == nil {
		t.Fatal("PreflightDeploy() let a preview through whose every hostname the box's proxy orders a publicly logged certificate for")
	}
	if !strings.Contains(err.Error(), `"perHostnamePreviewCertificates"`) {
		t.Errorf("the refusal reads %q, want it to name the option that accepts it", err)
	}
}

func TestAPreviewDeployThatOptedIntoPerHostnameCertificatesIsLetThrough(t *testing.T) {
	t.Parallel()

	if err := preflightTier(newBoxServingPreviews(t, "preview.example.com"), vps.Options{PerHostnamePreviewCertificates: true}, environment.TierPreview); err != nil {
		t.Errorf("PreflightDeploy() = %v, want a preview whose project accepted published hostnames let through", err)
	}
}

func TestAProductionDeployIsNeverRefusedForHowPreviewsAreCertified(t *testing.T) {
	t.Parallel()

	if err := preflightTier(newBoxServingPreviews(t, "preview.example.com"), vps.Options{}, environment.TierProduction); err != nil {
		t.Errorf("PreflightDeploy() = %v, want production let through: its hostnames are its own domains", err)
	}
}
