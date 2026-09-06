package envstore

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/ocelhq/ocel/cli/internal/console/httpapi"
	"github.com/ocelhq/ocel/cli/internal/resolve"
)

var ErrNoValue = errors.New("the console holds no value under that key")

type Value struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt int64  `json:"updatedAt"`
}

type Client struct {
	api *httpapi.Client
}

func New(baseURL string) *Client {
	return &Client{api: httpapi.New(baseURL)}
}

func (c *Client) List(ctx context.Context, accessToken, projectID string) ([]Value, error) {
	var values []Value
	if err := c.api.Get(ctx, c.path(projectID), accessToken, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func (c *Client) Get(ctx context.Context, accessToken, projectID, key string) (Value, error) {
	var value Value
	err := c.api.Get(ctx, c.keyPath(projectID, key), accessToken, &value)
	if httpapi.HasStatus(err, http.StatusNotFound) {
		return Value{}, ErrNoValue
	}
	if err != nil {
		return Value{}, err
	}
	return value, nil
}

func (c *Client) Set(ctx context.Context, accessToken, projectID, key, value string) error {
	return c.api.Put(ctx, c.keyPath(projectID, key), accessToken, map[string]string{"value": value}, nil)
}

func (c *Client) Delete(ctx context.Context, accessToken, projectID, key string) (bool, error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	if err := c.api.Delete(ctx, c.keyPath(projectID, key), accessToken, &out); err != nil {
		return false, err
	}
	return out.Deleted, nil
}

func (c *Client) path(projectID string) string {
	return "/api/projects/" + url.PathEscape(projectID) + "/env"
}

func (c *Client) keyPath(projectID, key string) string {
	return c.path(projectID) + "/" + url.PathEscape(key)
}

func FetchAccount(ctx context.Context, apiURL, token, projectID string) (resolve.Account, error) {
	values, err := New(apiURL).List(ctx, token, projectID)
	if err != nil {
		return resolve.Account{}, err
	}

	env := make(map[string]string, len(values))
	for _, value := range values {
		env[value.Key] = value.Value
	}
	return resolve.Account{ProjectID: projectID, EnvVars: env, APIURL: apiURL, Token: token}, nil
}
