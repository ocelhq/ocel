package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type envStream struct {
	resp   *http.Response
	reader *bufio.Reader
}

func subscribeEnv(ctx context.Context, leaderAddr string) (*envStream, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+leaderAddr+"/env", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("dev server answered %s", resp.Status)
	}
	return &envStream{resp: resp, reader: bufio.NewReader(resp.Body)}, nil
}

func (s *envStream) next() (map[string]string, error) {
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		payload, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data: ")
		if !ok {
			continue
		}
		var env map[string]string
		if err := json.Unmarshal([]byte(payload), &env); err != nil {
			return nil, fmt.Errorf("decode env update: %w", err)
		}
		return env, nil
	}
}

func (s *envStream) close() { s.resp.Body.Close() }
