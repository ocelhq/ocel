package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ocelhq/ocel/platform/aws/provider/linkpublish"
)

const (
	commandPublishLinks = "publish-links"
	commandPruneLinks   = "prune-links"
)

func runLinks(command string, in io.Reader, out io.Writer) error {
	var req linkpublish.Request
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return fmt.Errorf("read the publish request on stdin: %w", err)
	}

	ctx := context.Background()
	clients, err := linkpublish.Load(ctx, req.Region)
	if err != nil {
		return err
	}

	act := linkpublish.Apply
	if command == commandPruneLinks {
		act = linkpublish.Destroy
	}
	res, err := act(ctx, clients, req)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(res)
}

func links(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case commandPublishLinks, commandPruneLinks:
		return true, runLinks(args[0], os.Stdin, os.Stdout)
	}
	return false, nil
}
