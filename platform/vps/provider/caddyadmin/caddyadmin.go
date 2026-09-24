package caddyadmin

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	DrainExpired = "drain-expired"
	Drained      = "drained"
	Ungated      = "ungated"
)

const SocketMode = "0600"

const ForwardHandler = "reverse_proxy"

func Listen(socket string) string { return "unix/" + socket + "|" + SocketMode }

func Keeps(document []byte, socket string) error {
	var read struct {
		Admin *struct {
			Disabled bool   `json:"disabled"`
			Listen   string `json:"listen"`
		} `json:"admin"`
	}
	if err := json.Unmarshal(document, &read); err != nil {
		return fmt.Errorf("is not valid json: %w", err)
	}
	wanted := Listen(socket)
	moving := errors.New("declares no admin endpoint at " + wanted)
	if read.Admin == nil || read.Admin.Disabled {
		return moving
	}
	if read.Admin.Listen != wanted {
		return moving
	}
	return nil
}
