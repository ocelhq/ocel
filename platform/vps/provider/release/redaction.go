package release

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	LogsWithheld     = "(withheld: the values to redact from them could not be read)"
	redactedMinBytes = 8
)

func EnvCommand(name string) string {
	return "docker inspect --type container --format " + shell.Quote("{{json .Config.Env}}") + " " + shell.Quote(name)
}

func (c *Containers) readRedactedLogs(ctx context.Context, elevation ssh.Acting, name, command string) string {
	logs := c.box.Evidence(ctx, elevation, command)
	if logs == "" {
		return ""
	}
	read, err := c.box.Stream(ctx, elevation, EnvCommand(name), nil)
	var env []string
	if err != nil || read.Code != 0 || json.Unmarshal([]byte(read.Stdout), &env) != nil {
		return LogsWithheld
	}
	return redaction(env).Replace(logs)
}

func redaction(env []string) *strings.Replacer {
	type variable struct{ key, value string }
	var redacted []variable
	for _, pair := range env {
		key, value, _ := strings.Cut(pair, "=")
		if len(value) >= redactedMinBytes {
			redacted = append(redacted, variable{key: key, value: value})
		}
	}
	slices.SortStableFunc(redacted, func(a, b variable) int { return cmp.Compare(len(b.value), len(a.value)) })
	pairs := make([]string, 0, 2*len(redacted))
	for _, each := range redacted {
		pairs = append(pairs, each.value, "[redacted "+each.key+"]")
	}
	return strings.NewReplacer(pairs...)
}
