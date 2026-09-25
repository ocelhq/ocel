package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/gcp/provider/envidentity"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	requestWindow = 30 * time.Second
	headerWindow  = 10 * time.Second
)

type poller interface {
	Poll(ctx context.Context) error
}

func serving(syncer poller, errs io.Writer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "only a POST polls", http.StatusMethodNotAllowed)
			return
		}
		if err := syncer.Poll(r.Context()); err != nil {
			fmt.Fprintf(errs, "ocel envsync: %s\n", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func main() {
	port := os.Getenv(providerkit.InjectedPortName)
	if port == "" {
		fmt.Fprintf(os.Stderr, "ocel envsync: %s is not set, so there is no port to answer Cloud Scheduler on\n", providerkit.InjectedPortName)
		os.Exit(1)
	}
	syncer, err := newSyncer(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel envsync: %v\n", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: ":" + port, Handler: serving(syncer, os.Stderr), ReadHeaderTimeout: headerWindow}
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "ocel envsync: %v\n", err)
		os.Exit(1)
	}
}

func newSyncer(getenv func(string) string) (*envsource.Syncer, error) {
	named := map[string]string{}
	for _, name := range []string{providerkit.NamespaceEnvVar, ports.ProjectEnvVar, ports.RegionEnvVar} {
		named[name] = getenv(name)
		if named[name] == "" {
			return nil, fmt.Errorf("%s is not set, so there is no database to read registrations from or key to seal under", name)
		}
	}
	namespace, err := providerkit.ParseNamespace(named[providerkit.NamespaceEnvVar])
	if err != nil {
		return nil, err
	}
	class := providerkit.Class(getenv(ports.ClassEnvVar))
	switch class {
	case providerkit.ClassProduction, providerkit.ClassPreview:
	default:
		return nil, fmt.Errorf("%s is %q, want %s or %s", ports.ClassEnvVar, class, providerkit.ClassProduction, providerkit.ClassPreview)
	}
	clients := &ports.Clients{
		Namespace: namespace,
		Project:   named[ports.ProjectEnvVar],
		Region:    named[ports.RegionEnvVar],
	}
	return &envsource.Syncer{
		Store: values.Store{
			Records: ports.Records{Clients: clients},
			Sealer:  ports.Sealer{Clients: clients},
		},
		Class: class,
		Target: envsource.Target{
			Issuer: envidentity.Issuer{},
			Client: &http.Client{Timeout: requestWindow},
		},
	}, nil
}
