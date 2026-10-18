package conformance

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

type Suite struct {
	New func(ctx context.Context, settings provider.Settings) (provider.Provider, error)

	Server providerserver.Config

	Options provider.Options

	Binary string

	Certificates *CertificateChecks

	Logs *LogChecks
}

type constructor func(ctx context.Context, settings provider.Settings) (provider.Provider, error)

func Run(t *testing.T, suite Suite) {
	t.Helper()

	if suite.New == nil {
		t.Fatal("the suite has no constructor, so nothing here reaches the provider's ports or hooks. " +
			"A provider whose ports need an account behind them runs RunWithoutAccount at its unit tier and Run where the account is")
	}
	refuseIncompleteServer(t, suite)

	t.Run("rpc", func(t *testing.T) {
		runServedRPC(t, suite)
		t.Run("a run says what it would change and then what it is doing", func(t *testing.T) { runBootstrapStream(t, suite) })
	})
	t.Run("cost", func(t *testing.T) { runCost(t, suite) })
	t.Run("certificates", func(t *testing.T) { runCertificates(t, suite, suite.New) })
	t.Run("ports", func(t *testing.T) { RunPorts(t, construct(t, suite, suite.New)) })
	t.Run("hooks", func(t *testing.T) { RunHooks(t, construct(t, suite, suite.New)) })
	t.Run("logs", func(t *testing.T) { runLogs(t, suite) })
}

func RunWithoutAccount(t *testing.T, suite Suite) {
	t.Helper()

	if suite.New != nil || suite.Logs != nil {
		t.Fatal("the suite names a constructor or log checks, and only Run runs them: " +
			"a suite run without an account would report a pass having left them unexercised")
	}
	refuseIncompleteServer(t, suite)

	t.Run("rpc", func(t *testing.T) { runServedRPC(t, suite) })
	t.Run("cost", func(t *testing.T) { runCost(t, suite) })
	t.Run("certificates", func(t *testing.T) { runCertificates(t, suite, suite.Server.New) })
	t.Run("logs", func(t *testing.T) {
		p := construct(t, suite, suite.Server.New)
		RunLogs(t, p.Facts(), p.Logs())
	})
}

func refuseIncompleteServer(t *testing.T, suite Suite) {
	t.Helper()

	if suite.Server.New == nil {
		t.Fatal("the suite has no Server.New, so there is no RPC server to serve and nothing reads Facts() back off what it serves")
	}
	if suite.Binary == "" {
		t.Fatal("the suite names no binary, so nothing checks that the provider a CLI spawns pairs, serves and stops")
	}
}

func construct(t *testing.T, suite Suite, build constructor) provider.Provider {
	t.Helper()

	p, err := build(context.Background(), provider.Settings{Options: suite.Options})
	if err != nil {
		t.Fatalf("New() error = %v, want a provider", err)
	}
	return p
}

func RunHooks(t *testing.T, p provider.Provider) {
	t.Helper()

	hooks := p.Hooks()

	t.Run("ForwardPorts", func(t *testing.T) {
		if hooks.BindingAccess == nil {
			t.Skip("this provider forwards no ports, so a build gets no bindings to its private resources")
		}
		forwards, err := hooks.BindingAccess.ForwardPorts(context.Background(), provider.PortForwardRequest{}, progress.Discard())
		if err != nil || len(forwards) != 0 {
			t.Errorf("ForwardPorts() of no binding = %v, %v, want nothing forwarded and nothing reached", forwards, err)
		}
	})
	t.Run("Retirement", func(t *testing.T) {
		if hooks.Retirement == nil {
			t.Skip("this provider keeps nothing per app outside its releases, so an app removed from config leaves nothing behind")
		}
		RunRetirement(t, hooks.Retirement)
	})
	t.Run("NextServerRuntime", func(t *testing.T) {
		if hooks.NextServerRuntime == nil {
			t.Skip("this provider ships no Next server runtime, so its Next containers run Next with Next's defaults")
		}
		files, err := hooks.NextServerRuntime(context.Background())
		if err != nil {
			t.Fatalf("NextServerRuntime() error = %v", err)
		}
		if _, ok := files[containerimage.NextServerPreloadFile]; !ok {
			t.Errorf("NextServerRuntime() has no %s, which a Next container runs in front of next start", containerimage.NextServerPreloadFile)
		}
	})
}
