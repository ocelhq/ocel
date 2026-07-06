package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/resources/v1/resourcesv1connect"
)

type DevServer struct{}

var cache map[string]string

func (s *DevServer) Declare(_ context.Context, req *resourcesv1.DeclareRequest) (*resourcesv1.DeclareResponse, error) {
	res := &resourcesv1.DeclareResponse{}

	if req.Resource.Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
		return nil, fmt.Errorf("unsupported resource type: %v", req.Resource.Type)
	}

	cache[req.Resource.Name] = ""

	return res, nil
}

func (s *DevServer) Sync() {
	// called to run sync after discovery is complete
	// collects all resources in cache with their configs, makes api call to Ocel API
	// api handles assigning resources in parallel
}

// devCmd runs the current Ocel project in development mode.
var devCmd = &cobra.Command{
	Use:   "dev",
	Short: "Run your project in development mode",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintln(cmd.OutOrStdout(), "ocel dev: not implemented yet")

		// check auth status

		// if not authed, prompt to login on dashboard

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		cfg, err := projectconfig.Resolve(cwd)
		if err != nil {
			return err
		}
		_ = cfg // used by discovery, wired in a follow-up issue

		// detect language, framework, and run discovery

		devServer := &DevServer{}
		mux := http.NewServeMux()
		path, handler := resourcesv1connect.NewResourceServiceHandler(devServer)
		mux.Handle(path, handler)

		p := new(http.Protocols)
		p.SetHTTP1(true)
		p.SetUnencryptedHTTP2(true)

		srv := &http.Server{
			Addr:      ":8080",
			Handler:   mux,
			TLSConfig: nil,
		}

		srv.ListenAndServe()

		return nil
	},
}
