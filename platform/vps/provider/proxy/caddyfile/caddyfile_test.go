package caddyfile_test

import (
	"context"
	"errors"
	"strings"

	"github.com/ocelhq/ocel/pkg/router"
)

type ran struct {
	argv  []string
	stdin []byte
}

type box struct {
	ran      []ran
	said     map[string]string
	refused  map[string]string
	claimed  []string
	placed   string
	answers  map[string]router.Kind
	failures map[string]string
}

func (b *box) answer(argv []string, stdin []byte) (string, error) {
	b.ran = append(b.ran, ran{argv: argv, stdin: stdin})
	spoken := strings.Join(argv, " ")
	for prefix, refused := range b.refused {
		if strings.Contains(spoken, prefix) {
			return "", errors.New(refused)
		}
	}
	for prefix, said := range b.said {
		if strings.Contains(spoken, prefix) {
			return said, nil
		}
	}
	return "", nil
}

func (b *box) Ran(_ context.Context, _ string, argv []string) (string, error) {
	return b.answer(argv, nil)
}

func (b *box) RanWithStdin(_ context.Context, _ string, argv []string, stdin []byte) (string, error) {
	return b.answer(argv, stdin)
}

func (b *box) Claimed(context.Context) ([]string, error) { return b.claimed, nil }

func (b *box) PlacedSum(context.Context, string) (string, error) { return b.placed, nil }

func (b *box) Probe(_ context.Context, hostname string) (router.Kind, string, error) {
	return b.answers[hostname], b.failures[hostname], nil
}

func (b *box) argvs() [][]string {
	argvs := make([][]string, 0, len(b.ran))
	for _, each := range b.ran {
		argvs = append(argvs, each.argv)
	}
	return argvs
}
