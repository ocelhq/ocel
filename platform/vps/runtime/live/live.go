package live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
)

type Values = live.Values

const answerCeiling = 1 << 20

type socketSource struct {
	socket string
	client *http.Client
}

func (f *socketSource) Fetch(ctx context.Context) (map[string]string, error) {
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
		return nil, fmt.Errorf("the box answered %q for this deployment's values: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var answer vars.Answer
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("decode the box's values: %w", err)
	}
	return answer.Values, nil
}

func (f *socketSource) FreeSpace() (uint64, uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), live.FetchBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://ocel-live"+vars.SpacePath, nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("ask the box for the store volume's size over %s: %w", f.socket, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, answerCeiling))
	if err != nil {
		return 0, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("the box answered %q for the store volume: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var space vars.Space
	if err := json.Unmarshal(body, &space); err != nil {
		return 0, 0, fmt.Errorf("decode the box's volume size: %w", err)
	}
	return space.Free, space.Total, nil
}

func FreeSpace(socket string) func() (uint64, uint64, error) {
	return over(socket).FreeSpace
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
	return live.New(over(socket), live.Keys(manifest.Keys, manifest.Bindings), manifest.Bindings, nil)
}

func over(socket string) *socketSource {
	return &socketSource{socket: socket, client: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}}
}
