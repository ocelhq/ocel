package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	requestTimeout = 30 * time.Second
	headerTimeout  = 10 * time.Second
	logPrefix      = "ocel envsourcesync: "
)

func newHandler(copyScheduled func(context.Context) error, errs io.Writer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "only a POST syncs", http.StatusMethodNotAllowed)
			return
		}
		if err := copyScheduled(r.Context()); err != nil {
			fmt.Fprintf(errs, "%s%s\n", logPrefix, err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func main() {
	port := os.Getenv(appbuild.InjectedPortName)
	if port == "" {
		fmt.Fprintf(os.Stderr, "%s%s is not set, so there is no port to answer Cloud Scheduler on\n", logPrefix, appbuild.InjectedPortName)
		os.Exit(1)
	}
	sync, err := newSync(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s%v\n", logPrefix, err)
		os.Exit(1)
	}
	server := &http.Server{Addr: ":" + port, Handler: newHandler(sync.CopyScheduled, os.Stderr), ReadHeaderTimeout: headerTimeout}
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "%s%v\n", logPrefix, err)
		os.Exit(1)
	}
}

func newSync(getenv func(string) string) (*envsource.Sync, error) {
	named := map[string]string{}
	for _, name := range []string{provider.NamespaceEnvVar, ports.ProjectEnvVar, ports.RegionEnvVar} {
		named[name] = getenv(name)
		if named[name] == "" {
			return nil, fmt.Errorf("%s is not set, so there is no database to read registrations from or key to seal under", name)
		}
	}
	namespace, err := provider.ParseNamespace(named[provider.NamespaceEnvVar])
	if err != nil {
		return nil, err
	}
	class := edge.Class(getenv(ports.ClassEnvVar))
	switch class {
	case edge.ClassProduction, edge.ClassPreview:
	default:
		return nil, fmt.Errorf("%s is %q, want %s or %s", ports.ClassEnvVar, class, edge.ClassProduction, edge.ClassPreview)
	}
	clients := &ports.Clients{
		Namespace: namespace,
		Project:   named[ports.ProjectEnvVar],
		Region:    named[ports.RegionEnvVar],
	}
	return &envsource.Sync{
		Store: envvars.Store{
			Records: ports.Records{Clients: clients},
			Cipher:  ports.Cipher{Clients: clients},
		},
		Class: class,
		Login: envsource.Login{
			ProveIdentity: ports.ProveIdentity,
			Client:        &http.Client{Timeout: requestTimeout},
		},
	}, nil
}
