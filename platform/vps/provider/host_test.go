package vps_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh/sshtest"
)

func TestTraceFlagsGoToTheBoxOnlyWhenItsAgentIsThisBuilds(t *testing.T) {
	ctx, _ := tracingtest.Record(t)
	var installed string
	for _, item := range host.Items(vps.TestAssets, environment.TierProduction, nil, ssh.AMD64, front.Proxy{}) {
		if item.Kind == machine.KindFile && item.Name == host.LiveBinary {
			installed = item.Sum()
		}
	}
	if installed == "" {
		t.Fatalf("this build installs no %s", host.LiveBinary)
	}
	for _, box := range []struct {
		named string
		sum   string
		want  bool
	}{
		{named: "the agent this build installs", sum: installed, want: true},
		{named: "an agent a release before trace flags installed", sum: strings.Repeat("0", 64), want: false},
	} {
		answered := sshtest.New(func(command string) (ssh.Result, bool) {
			switch {
			case strings.HasSuffix(command, "uname -m"):
				return ssh.Result{Stdout: "x86_64\n"}, true
			case strings.Contains(command, "for p in") && strings.Contains(command, shell.Quote(host.LiveBinary)):
				return ssh.Result{Stdout: machine.KindFile + "\t" + host.LiveBinary + "\t755\troot\t" + box.sum + "\n"}, true
			}
			return ssh.Result{}, false
		})
		answered.SpanSocket = "/tmp/ocel-spans.abc/otlp.sock"
		p := vps.ProviderOver(vps.Options{}, answered.Dial)

		if got := p.Host().TraceFlags(ctx); (got != nil) != box.want {
			t.Errorf("TraceFlags() on a box running %s = %q, want flags %v: an ocel-live released before them refuses them, failing every seal, record and pin", box.named, got, box.want)
		}
	}
}
