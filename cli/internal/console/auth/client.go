package auth

import "github.com/ocelhq/ocel/cli/internal/console/httpapi"

const ClientID = "ocel-cli"

type Client struct {
	api *httpapi.Client
}

func New(baseURL string) *Client {
	return &Client{api: httpapi.New(baseURL)}
}
