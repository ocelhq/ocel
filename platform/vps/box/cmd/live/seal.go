package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/box/sealkey"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

const sealNamed = "ocel-live " + boxapi.SealCommand

func sealCommand(argv []string, stdin io.Reader, stdout, errs io.Writer) int {
	flags := flag.NewFlagSet(sealNamed, flag.ContinueOnError)
	flags.SetOutput(errs)
	tierRoot := flags.String("tier-root", boxapi.TierRoot, "where each tier keeps its seal key")
	if err := flags.Parse(argv); err != nil {
		return boxapi.ExitRefused
	}
	written, err := runSeal(*tierRoot, flags.Args(), stdin)
	if err != nil {
		fmt.Fprintf(errs, "%s: %s\n", sealNamed, err)
		return boxapi.ExitRefused
	}
	if written != nil {
		fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(written))
	}
	return 0
}

func runSeal(tierRoot string, args []string, stdin io.Reader) ([]byte, error) {
	if len(args) < 2 {
		return nil, errors.New("usage: " + sealNamed + " <tier> init|seal|open [<associated data>]")
	}
	tier, verb, rest := args[0], args[1], args[2:]
	if !isTier(tier) {
		return nil, fmt.Errorf("%s is not a valid tier", tier)
	}
	switch verb {
	case boxapi.SealVerbInit:
		if len(rest) != 0 {
			return nil, errors.New("init takes no associated data")
		}
		return nil, sealkey.Create(tierRoot, environment.Tier(tier))
	case boxapi.SealVerbSeal, boxapi.SealVerbOpen:
	default:
		return nil, fmt.Errorf("unknown verb %s", verb)
	}
	var export spanExport
	traced := flag.NewFlagSet(sealNamed+" "+verb, flag.ContinueOnError)
	traced.SetOutput(io.Discard)
	export.register(traced)
	if err := traced.Parse(rest); err != nil {
		return nil, err
	}
	if rest = traced.Args(); len(rest) != 1 {
		return nil, fmt.Errorf("%s takes the associated data as one argument", verb)
	}
	_, end := export.start(context.Background(), sealNamed, []string{tier, verb, rest[0]})
	written, err := sealOrOpen(tierRoot, environment.Tier(tier), verb, []byte(rest[0]), stdin)
	if err != nil {
		end(boxapi.ExitRefused)
		return nil, err
	}
	end(0)
	return written, nil
}

func sealOrOpen(tierRoot string, tier environment.Tier, verb string, bound []byte, stdin io.Reader) ([]byte, error) {
	key, err := sealkey.Read(tierRoot, tier)
	if err != nil {
		return nil, err
	}
	fed, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	body, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(fed)))
	if err != nil {
		return nil, errors.New("stdin is not base64")
	}
	if verb == boxapi.SealVerbSeal {
		return sealkey.SealBytes(key, bound, body)
	}
	return sealkey.OpenBytes(key, bound, body)
}
