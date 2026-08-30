package providerkit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type registryImages struct {
	target RegistryTarget
}

func RegistryImages(target RegistryTarget) ImageStore { return registryImages{target: target} }

var manifestTypes = []string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}

const (
	registryAttempts = 5
	registryBackoff  = 250 * time.Millisecond
	registryCeiling  = 8 * time.Second
)

var registryTimeout = 30 * time.Second

func (r registryImages) Has(ctx context.Context, push ImagePush) (bool, error) {
	server, repository, tag, err := splitCoordinate(push.Target)
	if err != nil {
		return false, err
	}
	client := &http.Client{Timeout: registryTimeout}
	endpoint := registryScheme(server) + "://" + server + "/v2/" + repository + "/manifests/" + url.PathEscape(tag)

	var wait time.Duration
	var held, again bool
	for attempt := range registryAttempts {
		if attempt > 0 {
			if err := pause(ctx, backoff(attempt, wait)); err != nil {
				return false, err
			}
		}
		held, again, wait, err = r.look(ctx, client, endpoint, server, repository, push)
		if !again {
			return held, err
		}
	}
	return false, err
}

func (r registryImages) look(ctx context.Context, client *http.Client, endpoint, server, repository string, push ImagePush) (held, again bool, after time.Duration, err error) {
	resp, err := r.head(ctx, client, endpoint, "")
	if err != nil {
		return false, addressable(err), 0, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		authorization, err := r.authorize(ctx, client, resp, server, repository)
		resp.Body.Close()
		if err != nil {
			return false, false, 0, err
		}
		if resp, err = r.head(ctx, client, endpoint, authorization); err != nil {
			return false, addressable(err), 0, err
		}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return answersFor(resp, push.Digest), false, 0, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, false, 0, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return false, true, retryAfter(resp), fmt.Errorf("%s answered %q asking whether it already holds %s", server, resp.Status, push.Target)
	default:
		return false, false, 0, fmt.Errorf("%s answered %q asking whether it already holds %s", server, resp.Status, push.Target)
	}
}

func answersFor(resp *http.Response, digest string) bool {
	answered := resp.Header.Get("Docker-Content-Digest")
	return answered == "" || answered == digest
}

func addressable(err error) bool {
	var dns *net.DNSError
	return !errors.As(err, &dns) || !dns.IsNotFound
}

func retryAfter(resp *http.Response) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func backoff(attempt int, asked time.Duration) time.Duration {
	wait := asked
	if wait <= 0 {
		wait = registryBackoff << (attempt - 1)
	}
	if wait > registryCeiling {
		wait = registryCeiling
	}
	return wait/2 + time.Duration(rand.Int64N(int64(wait/2)+1))
}

func pause(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r registryImages) head(ctx context.Context, client *http.Client, endpoint, authorization string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", strings.Join(manifestTypes, ", "))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ask %s whether it already holds this image: %w", endpoint, err)
	}
	return resp, nil
}

func (r registryImages) authorize(ctx context.Context, client *http.Client, refused *http.Response, server, repository string) (string, error) {
	scheme, params := challenge(refused.Header.Get("WWW-Authenticate"))
	switch strings.ToLower(scheme) {
	case "basic":
		return r.basic(), nil
	case "bearer":
		return r.bearer(ctx, client, params, server, repository)
	default:
		return "", fmt.Errorf("the registry refused an unauthenticated read and asked for %q, which ocel does not speak", refused.Header.Get("WWW-Authenticate"))
	}
}

func (r registryImages) basic() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(r.target.Username+":"+r.target.Password))
}

func (r registryImages) bearer(ctx context.Context, client *http.Client, params map[string]string, server, repository string) (string, error) {
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("the registry asked for a bearer token and named no realm to fetch it from")
	}
	if err := credentialsTravelTo(realm, server); err != nil {
		return "", err
	}
	query := url.Values{}
	if service := params["service"]; service != "" {
		query.Set("service", service)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repository + ":pull"
	}
	query.Set("scope", scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm+"?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	if r.target.Username != "" || r.target.Password != "" {
		req.SetBasicAuth(r.target.Username, r.target.Password)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch a token from %s: %w", realm, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %q handing out a token for %s: the registry credentials this deploy carries are not accepted", realm, resp.Status, repository)
	}
	var handed struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&handed); err != nil {
		return "", fmt.Errorf("read the token %s handed out: %w", realm, err)
	}
	token := handed.Token
	if token == "" {
		token = handed.AccessToken
	}
	if token == "" {
		return "", fmt.Errorf("%s handed out an empty token for %s", realm, repository)
	}
	return "Bearer " + token, nil
}

func credentialsTravelTo(realm, server string) error {
	at, err := url.Parse(realm)
	if err != nil {
		return fmt.Errorf("%s asked for a token to be bought at %q, which is no url ocel can reach: %w", server, realm, err)
	}
	switch {
	case at.Scheme == "https":
		return nil
	case at.Scheme == "http" && at.Host == server && registryScheme(server) == "http":
		return nil
	}
	return fmt.Errorf("%s asked for the deploy's registry password to be presented at %q, which is neither an https realm nor the registry itself: ocel hands that password to nobody else", server, realm)
}

func challenge(header string) (string, map[string]string) {
	scheme, rest, split := strings.Cut(strings.TrimSpace(header), " ")
	params := map[string]string{}
	if !split {
		return scheme, params
	}
	for _, pair := range unquotedFields(rest) {
		key, value, named := strings.Cut(strings.TrimSpace(pair), "=")
		if !named {
			continue
		}
		params[strings.ToLower(key)] = strings.Trim(value, `"`)
	}
	return scheme, params
}

func unquotedFields(value string) []string {
	var fields []string
	quoted, start := false, 0
	for i, r := range value {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			fields = append(fields, value[start:i])
			start = i + 1
		}
	}
	return append(fields, value[start:])
}

func splitCoordinate(coordinate string) (server, repository, tag string, err error) {
	host, path, split := strings.Cut(coordinate, "/")
	if !split {
		return "", "", "", fmt.Errorf("%q names no registry to push to", coordinate)
	}
	colon := strings.LastIndex(path, ":")
	if colon < 0 {
		return "", "", "", fmt.Errorf("%q carries no tag, and an image is pushed under one", coordinate)
	}
	return host, path[:colon], path[colon+1:], nil
}

func registryScheme(server string) string {
	host, _, split := strings.Cut(server, ":")
	if !split {
		host = server
	}
	switch host {
	case "localhost", "127.0.0.1", "[::1]", "::1":
		return "http"
	}
	return "https"
}

func (r registryImages) Push(ctx context.Context, push ImagePush, report Reporter) error {
	host, err := OpenDockerHost()
	if err != nil {
		return err
	}
	transport := host.Transport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	server, repository, tag, err := splitCoordinate(push.Target)
	if err != nil {
		return err
	}
	named := server + "/" + repository
	if err := host.Tag(ctx, client, push.Source, named, tag); err != nil {
		return err
	}
	return r.upload(ctx, client, host, named, tag, report)
}

func (r registryImages) upload(ctx context.Context, client *http.Client, host DockerHost, named, tag string, report Reporter) error {
	endpoint := "http://docker/images/" + named + "/push?" + url.Values{"tag": {tag}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	authorization, err := r.registryAuth()
	if err != nil {
		return err
	}
	req.Header.Set("X-Registry-Auth", authorization)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("push %s:%s from the daemon at %s: %w", named, tag, host.Address, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the daemon at %s answered %q pushing %s:%s: %s", host.Address, resp.Status, named, tag, said(resp.Body))
	}
	return drainPush(resp.Body, report)
}

func (r registryImages) registryAuth() (string, error) {
	encoded, err := json.Marshal(map[string]string{
		"username":      r.target.Username,
		"password":      r.target.Password,
		"serveraddress": r.target.Server,
	})
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(encoded), nil
}

func drainPush(body io.Reader, report Reporter) error {
	decoder := json.NewDecoder(body)
	for {
		var line struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := decoder.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read the daemon's push progress: %w", err)
		}
		if line.Error != "" {
			return fmt.Errorf("the registry refused the push: %s", line.Error)
		}
		if report != nil && line.Status != "" {
			report.Detail(line.Status)
		}
	}
}

func said(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, 4096))
	return strings.TrimSpace(string(raw))
}
