package release

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	UnstartedMarker   = "unstarted"
	CrashLoopRestarts = 3
	restartFormat     = "{{.State.Status}} {{.RestartCount}}"
)

var unstartedStatuses = []string{"restarting", "exited", "dead"}

func RenderStartWatch(names []string, probe []string) string {
	var checks strings.Builder
	for _, name := range names {
		checks.WriteString("\tcrashed " + shell.Quote(name) + "\n")
	}
	return "sh -c " + shell.Quote(shell.Words(probe)+" &\n"+
		"probe=$!\n"+
		"crashed() {\n"+
		"\tset -- \"$1\" $(docker inspect --type container --format "+shell.Quote(restartFormat)+" \"$1\" 2>/dev/null)\n"+
		"\tcase $2 in\n"+
		"\texited | dead) ;;\n"+
		"\trestarting) [ \"${3:-0}\" -ge "+strconv.Itoa(CrashLoopRestarts)+" ] || return 0 ;;\n"+
		"\t*) return 0 ;;\n"+
		"\tesac\n"+
		"\tkill \"$probe\" 2>/dev/null\n"+
		"\tprintf '%s %s\\n' "+shell.Quote(UnstartedMarker)+" \"$1\"\n"+
		"\texit 1\n"+
		"}\n"+
		"while kill -0 \"$probe\" 2>/dev/null; do\n"+
		checks.String()+
		"\tsleep 1\n"+
		"done\n"+
		"wait \"$probe\"")
}

func FindUnstarted(said string) (string, bool) {
	for line := range strings.Lines(said) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == UnstartedMarker {
			return fields[1], true
		}
	}
	return "", false
}

func isUnstarted(state string) bool {
	for _, status := range unstartedStatuses {
		if strings.HasPrefix(state, "Status="+status+" ") {
			return true
		}
	}
	return false
}

func RenderLastRunLogs(name string) string {
	return "started=$(docker inspect --type container --format '{{.State.StartedAt}}' " + shell.Quote(name) + ") && " +
		"docker logs --since \"$started\" --tail " + docker.LogTail + " " + shell.Quote(name) + " 2>&1 || true"
}

func (c *Containers) describeUnstarted(ctx context.Context, app, name, state string, elevation ssh.Acting) string {
	logs := c.box.Evidence(ctx, elevation, RenderLastRunLogs(name))
	ran := "its last run, last " + docker.LogTail + " lines"
	if logs == "" {
		logs = c.box.Evidence(ctx, elevation, LogCommand(name))
		ran = "the last " + docker.LogTail + " lines"
	}
	if logs == "" {
		logs = NoLogOutput
	}
	return fmt.Sprintf("%s's container exited before it ever answered HTTP, so the app is failing to start, not failing a health check\nstate: %s\nlogs (%s):\n%s",
		app, state, ran, logs)
}
