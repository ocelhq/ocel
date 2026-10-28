package pulumi

import (
	"context"
	"io"
	"slices"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/tracing"
)

const maxCommandWords = 2

type tracedCommand struct {
	auto.PulumiCommand
	secrets   []string
	collector tracing.Collector
}

func (c tracedCommand) Run(ctx context.Context, workdir string, stdin io.Reader, additionalOutput, additionalErrorOutput []io.Writer, additionalEnv []string, args ...string) (string, string, int, error) {
	ctx, span := tracer.Start(ctx, commandName(args))
	if c.collector.URL != "" {
		relay, err := startSpanRelay(c.collector)
		if err != nil {
			span.RecordError(err)
		} else {
			defer func() { _ = relay.Close() }()
			args = append([]string{otelTracesFlag, relay.endpoint}, args...)
			c.secrets = append(slices.Clip(c.secrets), relay.token)
		}
	}
	span.SetAttributes(
		tracing.CommandArgsKey.StringSlice(c.masked(append([]string{"pulumi"}, args...))),
		tracing.WorkingDirectoryKey.String(workdir),
	)

	env := slices.Concat(additionalEnv, tracing.ParentEnv(ctx))
	stdout, stderr, code, err := c.PulumiCommand.Run(ctx, workdir, stdin, additionalOutput, additionalErrorOutput, env, args...)
	span.SetAttributes(tracing.ExitCodeKey.Int(code))
	var failure error
	if err != nil {
		span.SetAttributes(tracing.StderrTailKey.String(c.mask(tracing.NewTail(stderr).String())))
		failure = maskedMessage(c.mask(err.Error()))
	}
	tracing.EndSpan(span, failure)
	return stdout, stderr, code, err
}

func (c tracedCommand) mask(s string) string { return maskSecrets(s, c.secrets) }

func (c tracedCommand) masked(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = c.mask(v)
	}
	return out
}

func commandName(args []string) string {
	words := []string{"pulumi"}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") || len(words) > maxCommandWords {
			break
		}
		words = append(words, arg)
	}
	return strings.Join(words, " ")
}
