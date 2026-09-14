package live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	rt "github.com/ocelhq/ocel/pkg/runtimekit/live"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
)

type Values = rt.Values

const answerCeiling = 1 << 20

type socketFetcher struct {
	socket string
	client *http.Client
}

func (f *socketFetcher) FetchLive(ctx context.Context) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://ocel-live"+vars.ValuesPath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ask the box for this deployment's values over %s: %w", f.socket, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, answerCeiling))
	if err != nil {
		return nil, fmt.Errorf("read the box's answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the box answered %q asking for this deployment's values: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var answer vars.Answer
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("the box answered something that is not a value set: %w", err)
	}
	return answer.Values, nil
}

func FromManifest(raw []byte, socket string) (*Values, error) {
	manifest, err := vars.Parse(raw)
	if err != nil {
		return nil, err
	}
	if !manifest.Live() {
		return nil, nil
	}
	return Over(manifest, socket), nil
}

func Over(manifest vars.Manifest, socket string) *Values {
	fetcher := &socketFetcher{socket: socket, client: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}}
	return rt.New(fetcher, rt.Keys(manifest.Keys, manifest.Bindings), manifest.Bindings, nil)
}
