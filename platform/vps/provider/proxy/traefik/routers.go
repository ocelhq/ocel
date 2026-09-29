package traefik

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/Masterminds/sprig/v3"
	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type userRouter struct {
	name  string
	rule  string
	where string
}

type collision struct {
	hostname string
	router   userRouter
}

func (t Traefik) refuseRouted(ctx context.Context, hostnames []string) error {
	routers, unread, err := t.routers(ctx)
	if err != nil {
		return err
	}
	if len(unread) > 0 {
		lines := make([]string, 0, len(unread))
		for _, file := range unread {
			lines = append(lines, file.said())
		}
		return refusal.Refuse(refusal.CodeInvalid,
			"%s\nOcel never takes a hostname your Traefik may already route, and a router in a file it cannot read may route %s; fix or remove that file",
			strings.Join(lines, "\n"), strings.Join(hostnames, ", "))
	}
	found := collisions(hostnames, routers)
	if len(found) == 0 {
		return nil
	}
	lines := make([]string, 0, len(found))
	for _, each := range found {
		lines = append(lines, each.said())
	}
	return refusal.Refuse(refusal.CodeBusy,
		"%s\nOcel never takes a hostname your Traefik routes, because its routers outrank yours; remove that router, or bind a hostname nothing routes yet",
		strings.Join(lines, "\n"))
}

func (c collision) said() string {
	return fmt.Sprintf("%s is already routed by your Traefik: router %s in %s", c.hostname, c.router.name, c.router.where)
}

type unreadFile struct {
	path string
	err  error
}

func (u unreadFile) said() string {
	return fmt.Sprintf("ocel cannot read %s for the routers in it: %s", u.path, oneLine(u.err))
}

func (t Traefik) routers(ctx context.Context) ([]userRouter, []unreadFile, error) {
	beside, err := t.Box.ReadBeside(ctx, t.file())
	if err != nil {
		return nil, nil, err
	}
	var found []userRouter
	var unread []unreadFile
	for _, file := range beside {
		path := filepath.Join(t.directory(), file.Name)
		tree, err := decodeDynamic(file.Name, file.Content)
		if err != nil {
			unread = append(unread, unreadFile{path: path, err: err})
			continue
		}
		found = append(found, routersIn(tree, path)...)
	}
	for _, source := range []struct {
		what  string
		argv  []string
		where string
	}{
		{what: "read the routers your containers' labels declare", argv: containerLabels(), where: "container"},
		{what: "read the routers your swarm services' labels declare", argv: serviceLabels(), where: "swarm service"},
	} {
		said, err := t.Box.Ran(ctx, source.what, source.argv)
		if err != nil {
			return nil, nil, err
		}
		labelled, err := labelledIn(said, source.where)
		if err != nil {
			return nil, nil, err
		}
		found = append(found, labelled...)
	}
	return found, unread, nil
}

var errOnlyTraefikSees = errors.New("it reads what only your Traefik sees")

func templateFunctions() template.FuncMap {
	functions := sprig.TxtFuncMap()
	functions["normalize"] = normalize
	functions["split"] = strings.Split
	for _, unseen := range []string{"env", "expandenv", "getHostByName"} {
		functions[unseen] = func(...any) (string, error) {
			return "", fmt.Errorf("%s: %w", unseen, errOnlyTraefikSees)
		}
	}
	return functions
}

func normalize(name string) string {
	return strings.Join(strings.FieldsFunc(name, func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsNumber(c) }), "-")
}

func decodeDynamic(name string, content []byte) (map[string]any, error) {
	parsed, err := template.New(name).Funcs(templateFunctions()).Parse(string(content))
	if err != nil {
		return nil, err
	}
	var executed bytes.Buffer
	if err := parsed.Execute(&executed, false); err != nil {
		return nil, err
	}
	tree := map[string]any{}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".toml":
		if _, err := toml.Decode(executed.String(), &tree); err != nil {
			return nil, err
		}
	default:
		if err := yaml.Unmarshal(executed.Bytes(), &tree); err != nil {
			return nil, err
		}
	}
	return tree, nil
}

func keyed(tree any, key string) any {
	mapping, ok := tree.(map[string]any)
	if !ok {
		return nil
	}
	for name, value := range mapping {
		if strings.EqualFold(name, key) {
			return value
		}
	}
	return nil
}

func routersIn(tree map[string]any, where string) []userRouter {
	routers, ok := keyed(keyed(tree, "http"), "routers").(map[string]any)
	if !ok {
		return nil
	}
	var found []userRouter
	for name, declared := range routers {
		if rule, ok := keyed(declared, "rule").(string); ok {
			found = append(found, userRouter{name: name, rule: rule, where: where})
		}
	}
	return found
}

const (
	routerLabel = "traefik.http.routers."
	ruleLabel   = ".rule"
	enableLabel = "traefik.enable"
)

func labelledIn(said, where string) ([]userRouter, error) {
	var found []userRouter
	for line := range strings.Lines(said) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		reading := json.NewDecoder(strings.NewReader(line))
		var name string
		var labels map[string]string
		if err := reading.Decode(&name); err != nil {
			return nil, fmt.Errorf("read the labels docker reported in %q: %w", line, err)
		}
		if err := reading.Decode(&labels); err != nil && err != io.EOF {
			return nil, fmt.Errorf("read the labels docker reported in %q: %w", line, err)
		}
		if isIgnored(labels) {
			continue
		}
		for key, rule := range labels {
			lowered := strings.ToLower(key)
			router, labelledRule := strings.CutPrefix(lowered, routerLabel)
			if !labelledRule || !strings.HasSuffix(router, ruleLabel) {
				continue
			}
			found = append(found, userRouter{
				name:  key[len(routerLabel) : len(key)-len(ruleLabel)],
				rule:  rule,
				where: where + " " + strings.TrimPrefix(name, "/"),
			})
		}
	}
	return found, nil
}

func isIgnored(labels map[string]string) bool {
	for key, value := range labels {
		if strings.EqualFold(key, enableLabel) && strings.EqualFold(strings.TrimSpace(value), "false") {
			return true
		}
	}
	return false
}

func collisions(hostnames []string, routers []userRouter) []collision {
	slices.SortFunc(routers, func(a, b userRouter) int {
		return strings.Compare(a.where+"\x00"+a.name, b.where+"\x00"+b.name)
	})
	var found []collision
	for _, hostname := range hostnames {
		for _, router := range routers {
			if matches(router.rule, hostname) {
				found = append(found, collision{hostname: hostname, router: router})
			}
		}
	}
	return found
}

const unclaimedHostname = "ocel-unrouted.invalid"

var matcher = regexp.MustCompile(`(?i)\b(HostRegexp|HostHeader|Host)\s*\(`)

func matches(rule, hostname string) bool {
	for _, at := range matcher.FindAllStringSubmatchIndex(rule, -1) {
		function := strings.ToLower(rule[at[2]:at[3]])
		for _, argument := range arguments(rule[at[1]:]) {
			if function != "hostregexp" {
				if strings.EqualFold(argument, hostname) {
					return true
				}
				continue
			}
			pattern, err := regexp.Compile(argument)
			if err == nil && pattern.MatchString(strings.ToLower(hostname)) && !pattern.MatchString(unclaimedHostname) {
				return true
			}
		}
	}
	return false
}

func arguments(rest string) []string {
	var found []string
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return found
		}
		var argument string
		switch rest[0] {
		case '`':
			end := strings.IndexByte(rest[1:], '`')
			if end < 0 {
				return found
			}
			argument, rest = rest[1:end+1], rest[end+2:]
		case '"':
			quoted, err := strconv.QuotedPrefix(rest)
			if err != nil {
				return found
			}
			argument, _ = strconv.Unquote(quoted)
			rest = rest[len(quoted):]
		default:
			return found
		}
		found = append(found, argument)
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, ",") {
			return found
		}
		rest = rest[1:]
	}
}
