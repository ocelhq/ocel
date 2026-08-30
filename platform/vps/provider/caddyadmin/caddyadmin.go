package caddyadmin

import (
	"encoding/json"
	"errors"
	"fmt"
)

const DrainExpired = "drain-expired"

const SocketMode = "0600"

func Listen(socket string) string { return "unix/" + socket + "|" + SocketMode }

func Keeps(document []byte, socket string) error {
	var read struct {
		Admin *struct {
			Disabled bool   `json:"disabled"`
			Listen   string `json:"listen"`
		} `json:"admin"`
	}
	if err := json.Unmarshal(document, &read); err != nil {
		return fmt.Errorf("is not json a proxy could load: %w", err)
	}
	wanted := Listen(socket)
	moving := errors.New("declares no admin endpoint at " + wanted +
		", and caddy moves the admin endpoint before it validates the rest: a config without one takes the socket this helper is reached through with it and opens a tcp listener in its place, and one that names the socket without the mode leaves the permissions that are the whole of its access control to whatever the proxy happens to default to")
	if read.Admin == nil || read.Admin.Disabled {
		return moving
	}
	if read.Admin.Listen != wanted {
		return moving
	}
	return nil
}
