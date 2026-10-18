package host

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	unstartedMarker   = "unstarted"
	crashLoopRestarts = 3
	restartFormat     = "{{.State.Status}} {{.RestartCount}}"
)

var unstartedStatuses = []string{"restarting", "exited", "dead"}

func renderStartWatch(names []string, probe []string) string {
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
		"\trestarting) [ \"${3:-0}\" -ge "+strconv.Itoa(crashLoopRestarts)+" ] || return 0 ;;\n"+
		"\t*) return 0 ;;\n"+
		"\tesac\n"+
		"\tkill \"$probe\" 2>/dev/null\n"+
		"\tprintf '%s %s\\n' "+shell.Quote(unstartedMarker)+" \"$1\"\n"+
		"\texit 1\n"+
		"}\n"+
		"while kill -0 \"$probe\" 2>/dev/null; do\n"+
		checks.String()+
		"\tsleep 1\n"+
		"done\n"+
		"wait \"$probe\"")
}

func findUnstarted(said string) (string, bool) {
	for line := range strings.Lines(said) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == unstartedMarker {
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

func renderLastRunLogs(name string) string {
	return "started=$(docker inspect --type container --format '{{.State.StartedAt}}' " + shell.Quote(name) + ") && " +
		"docker logs --since \"$started\" --tail " + appLogTail + " " + shell.Quote(name) + " 2>&1 || true"
}

func (h *Host) describeUnstarted(ctx context.Context, app, name, state string, elevation ssh.Acting) string {
	logs := h.Evidence(ctx, elevation, renderLastRunLogs(name))
	ran := "its last run, last " + appLogTail + " lines"
	if logs == "" {
		logs = h.Evidence(ctx, elevation, logCommand(name))
		ran = "the last " + appLogTail + " lines"
	}
	if logs == "" {
		logs = noLogOutput
	}
	return fmt.Sprintf("%s's container exited before it ever answered HTTP, so the app is failing to start, not failing a health check\nstate: %s\nlogs (%s):\n%s",
		app, state, ran, logs)
}
