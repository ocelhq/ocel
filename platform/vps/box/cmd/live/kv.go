package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/vps/box/recordstore"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

const recordsNamed = "ocel-live " + boxapi.RecordsCommand

func recordsCommand(argv []string, stdin io.Reader, stdout, errs io.Writer) int {
	flags := flag.NewFlagSet(recordsNamed, flag.ContinueOnError)
	flags.SetOutput(errs)
	stateRoot := flags.String("state-root", boxapi.StateRoot, "where each tier keeps its records")
	var export spanExport
	export.register(flags)
	if err := flags.Parse(argv); err != nil {
		return boxapi.ExitRefused
	}
	ctx, end := export.start(context.Background(), recordsNamed, flags.Args())
	code := printRecords(ctx, recordstore.KeyValues{Root: *stateRoot}, flags.Args(), stdin, stdout, errs)
	end(code)
	return code
}

func printRecords(ctx context.Context, store recordstore.KeyValues, args []string, stdin io.Reader, stdout, errs io.Writer) int {
	printed, err := runRecords(ctx, store, args, stdin)
	switch {
	case errors.Is(err, records.ErrNotFound):
		return boxapi.ExitNotFound
	case errors.Is(err, records.ErrStale):
		return boxapi.ExitStale
	case err != nil:
		fmt.Fprintf(errs, "%s: %s\n", recordsNamed, err)
		return boxapi.ExitRefused
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	for _, line := range printed {
		if err := encoder.Encode(line); err != nil {
			fmt.Fprintf(errs, "%s: %s\n", recordsNamed, err)
			return boxapi.ExitRefused
		}
	}
	return 0
}

func runRecords(ctx context.Context, store recordstore.KeyValues, args []string, stdin io.Reader) ([]any, error) {
	if len(args) < 2 {
		return nil, errors.New("usage: " + recordsNamed + " <tier> get <path> | list <partition dir> [<under>] | cas")
	}
	tier, verb, rest := args[0], args[1], args[2:]
	if !isTier(tier) {
		return nil, fmt.Errorf("%s is not a valid tier", tier)
	}
	switch verb {
	case boxapi.RecordsVerbGet:
		if len(rest) != 1 {
			return nil, errors.New("get takes one path")
		}
		file, err := store.ReadPath(ctx, environment.Tier(tier), rest[0])
		if err != nil {
			return nil, err
		}
		return []any{file}, nil
	case boxapi.RecordsVerbList:
		if len(rest) < 1 || len(rest) > 2 {
			return nil, errors.New("list takes a partition directory and, optionally, a path under it")
		}
		under := ""
		if len(rest) == 2 {
			under = rest[1]
		}
		listed, err := store.ListPath(ctx, environment.Tier(tier), rest[0], under)
		if err != nil {
			return nil, err
		}
		printed := make([]any, 0, len(listed))
		for _, entry := range listed {
			printed = append(printed, entry)
		}
		return printed, nil
	case boxapi.RecordsVerbCompareAndSet:
		if len(rest) != 0 {
			return nil, errors.New("cas takes its changes on stdin")
		}
		changes, err := readChanges(stdin)
		if err != nil {
			return nil, err
		}
		revisions, err := store.CompareAndSet(ctx, environment.Tier(tier), changes)
		if err != nil {
			return nil, err
		}
		return []any{revisions}, nil
	}
	return nil, fmt.Errorf("unknown verb %s", verb)
}

func readChanges(stdin io.Reader) ([]boxapi.Change, error) {
	decoder := json.NewDecoder(stdin)
	decoder.DisallowUnknownFields()
	var changes []boxapi.Change
	if err := decoder.Decode(&changes); err != nil {
		return nil, fmt.Errorf("stdin is not one JSON array of changes: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("stdin holds more than one JSON array of changes")
	}
	return changes, nil
}
