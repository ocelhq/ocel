package images

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func ProbePushAccess(ctx context.Context, target provider.RegistryTarget, repository string) error {
	store := registryStore{target: target}
	server, repo, _, err := splitImageRef(target.ImageRef(repository, "access"))
	if err != nil {
		return err
	}
	named := server + "/" + repo
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: registryTimeout}
	endpoint := registryScheme(server) + "://" + server + "/v2/" + repo + "/blobs/uploads/"

	resp, err := startUpload(ctx, client, endpoint, "")
	if err != nil {
		return refusal.Refuse(refusal.CodeNotReady, "could not reach %s to check that %s can push to it: %v", server, pusher(target), err)
	}
	authorization := ""
	if resp.StatusCode == http.StatusUnauthorized {
		authorization, err = store.authorize(ctx, client, resp, server, repo, "pull,push")
		resp.Body.Close()
		if err != nil {
			return refusal.Refuse(refusal.CodeDenied, "%s refused %s a token to push to %s: %v", server, pusher(target), named, err)
		}
		if resp, err = startUpload(ctx, client, endpoint, authorization); err != nil {
			return refusal.Refuse(refusal.CodeNotReady, "could not reach %s to check that %s can push to it: %v", server, pusher(target), err)
		}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusCreated:
		cancelUpload(ctx, client, resp, authorization)
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return refusal.Refuse(refusal.CodeDenied, "%s refuses %s a push to %s: %s", server, pusher(target), named, registrySaid(resp))
	default:
		return refusal.Refuse(refusal.CodeNotReady, "%s answered %s when %s started a push to %s", server, registrySaid(resp), pusher(target), named)
	}
}

func startUpload(ctx context.Context, client *http.Client, endpoint, authorization string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return client.Do(req)
}

func cancelUpload(ctx context.Context, client *http.Client, started *http.Response, authorization string) {
	location, err := started.Location()
	if err != nil || location.Scheme != started.Request.URL.Scheme || location.Host != started.Request.URL.Host {
		return
	}
	req, err := http.NewRequestWithContext(context.WithoutCancel(ctx), http.MethodDelete, location.String(), nil)
	if err != nil {
		return
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func pusher(target provider.RegistryTarget) string {
	if target.Username == "" {
		return "the registry password"
	}
	return target.Username
}

func registrySaid(resp *http.Response) string {
	var answered struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&answered); err != nil || len(answered.Errors) == 0 {
		return fmt.Sprintf("%q", resp.Status)
	}
	said := make([]string, 0, len(answered.Errors))
	for _, e := range answered.Errors {
		said = append(said, strings.TrimSpace(e.Code+": "+e.Message))
	}
	return strings.Join(said, "; ")
}
