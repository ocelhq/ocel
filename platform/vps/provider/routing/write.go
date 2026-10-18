package routing

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type Pair struct {
	Table   []byte
	Config  []byte
	Origins []proxy.OriginFile
}

func (d Pair) Digest() Digest { return Digest(boxapi.Sum(d.Table)) }

type Digest string

const (
	ExitMoved       = 9
	ExitUnseeded    = 10
	ExitPlaceFailed = 11
	Lock            = boxapi.StateRoot
)

func Locked(mode string) string {
	return shell.Locked(Lock, mode, shell.RoutingLockSeconds)
}

func PairReading() string {
	printed := func(path string) string {
		at := shell.Quote(path)
		return "if [ -f " + at + " ]; then printf '+'; base64 < " + at + " | tr -d '\\n'; fi; printf '\\n'"
	}
	return strings.Join([]string{
		"set -e",
		strings.TrimSuffix(Locked("-s"), "\n"),
		printed(boxapi.RoutingTablePath),
		printed(boxapi.ProxyConfig),
	}, "\n")
}

func (t *Table) currentPair(ctx context.Context) (Pair, error) {
	said, err := t.box.Run(ctx, ssh.RootWhenAble, "read "+boxapi.RoutingTablePath+" and "+boxapi.ProxyConfig, PairReading(), nil)
	if err != nil {
		return Pair{}, err
	}
	lines := strings.SplitN(said, "\n", 3)
	if len(lines) < 3 {
		return Pair{}, refusal.Refuse(refusal.CodeNotReady,
			"%s and %s on %s read back as %d lines (%d bytes), not one line for each", boxapi.RoutingTablePath, boxapi.ProxyConfig, t.box.Named(), strings.Count(said, "\n"), len(said))
	}
	var read [2][]byte
	for at, line := range lines[:2] {
		encoded, present := strings.CutPrefix(line, "+")
		if !present {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return Pair{}, refusal.Refuse(refusal.CodeNotReady,
				"%s and %s on %s read back undecodable: %v", boxapi.RoutingTablePath, boxapi.ProxyConfig, t.box.Named(), err)
		}
		read[at] = decoded
	}
	return Pair{Table: read[0], Config: read[1]}, nil
}

func (t *Table) CurrentTable(ctx context.Context) (Pair, error) {
	pair, err := t.currentPair(ctx)
	if err != nil {
		return Pair{}, err
	}
	if pair.Table == nil {
		return Pair{}, refusal.Refuse(refusal.CodeNotReady,
			"%s is missing on %s\nRun `ocel bootstrap` for this box's tier", boxapi.RoutingTablePath, t.box.Named())
	}
	return pair, nil
}

const Rewrites = 5

type Composed struct {
	restoring   Pair
	written     Digest
	FailedPlace error
	Changed     bool
	reloading   bool
	served      proxy.Spec
	admitted    proxy.Spec
}

func (t *Table) Compose(ctx context.Context, compose func(boxapi.RoutingTable) (boxapi.RoutingTable, error)) (Composed, error) {
	rewrites := 0
	at := Destination(t.front)
	for {
		pair, err := t.CurrentTable(ctx)
		if err != nil {
			return Composed{}, err
		}
		table, err := ReadRoutingTable(pair.Table)
		if err != nil {
			return Composed{}, err
		}
		shaped := Composed{restoring: pair, written: pair.Digest()}
		next, err := compose(table)
		if err != nil {
			return shaped, err
		}
		if added := slices.DeleteFunc(next.Hostnames(), func(hostname string) bool {
			return slices.Contains(table.Hostnames(), hostname)
		}); len(added) > 0 {
			if err := t.front.RefuseRouted(ctx, added); err != nil {
				return shaped, err
			}
		}
		before, err := WriteRoutingTable(table)
		if err != nil {
			return shaped, err
		}
		after, err := WriteRoutingTable(next)
		if err != nil {
			return shaped, err
		}
		rendered, err := RenderProxyConfig(t.front, table)
		if err != nil {
			return shaped, err
		}
		fresh := bytes.Equal(pair.Config, rendered)
		if at != "" {
			sum, err := t.DestinationSum(ctx, at)
			if err != nil {
				return shaped, err
			}
			fresh, shaped.restoring.Config = sum == boxapi.Sum(rendered), rendered
			if shaped.restoring.Origins, err = RenderOriginFiles(t.front, table); err != nil {
				return shaped, err
			}
		}
		if bytes.Equal(before, after) && fresh {
			return shaped, nil
		}
		if bytes.Equal(before, after) && at != "" {
			if err := t.front.Validate(ctx, rendered); err != nil {
				return shaped, err
			}
			shaped.reloading, shaped.admitted = true, Spec(table)
			err = t.replace(ctx, pair.Digest(), at, rendered, shaped.restoring.Origins)
		} else {
			var admitted []byte
			if admitted, err = RenderProxyConfig(t.front, next); err != nil {
				return shaped, err
			}
			var origins []proxy.OriginFile
			if origins, err = RenderOriginFiles(t.front, next); err != nil {
				return shaped, err
			}
			shaped.reloading = !bytes.Equal(shaped.restoring.Config, admitted) || at != "" && !fresh
			if at != "" && shaped.reloading {
				if err := t.front.Validate(ctx, admitted); err != nil {
					return shaped, err
				}
			}
			if shaped.admitted = Spec(next); fresh {
				shaped.served = Spec(table)
			}
			shaped.written, shaped.FailedPlace, err = t.writePair(ctx, pair.Digest(), Pair{Table: after, Config: admitted, Origins: origins}, shaped.reloading)
			shaped.Changed = true
		}
		rewrites++
		if err == nil || !Moved(err) || rewrites >= Rewrites {
			return shaped, err
		}
	}
}

func PairFed(pair Pair) string {
	return base64.StdEncoding.EncodeToString(pair.Table) + "\n" + base64.StdEncoding.EncodeToString(pair.Config) + "\n" + OriginsFed(pair.Origins)
}

func OriginsFed(origins []proxy.OriginFile) string {
	var fed strings.Builder
	for _, origin := range origins {
		fed.WriteString(base64.StdEncoding.EncodeToString(origin.Bundle) + "\n")
	}
	return fed.String()
}

func OriginPaths(origins []proxy.OriginFile) []string {
	paths := make([]string, 0, len(origins))
	for _, origin := range origins {
		paths = append(paths, origin.Path)
	}
	return paths
}

func originReading(paths []string) []string {
	reads := make([]string, 0, len(paths))
	for at := range paths {
		reads = append(reads, `IFS= read -r origin`+strconv.Itoa(at))
	}
	return reads
}

func originPlacing(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	dir := filepath.Dir(paths[0])
	steps := []string{`if ! group=$(` + shell.Words(Placement(dir, "origin-group")) + ` </dev/null); then exit ` + strconv.Itoa(ExitPlaceFailed) + `; fi`}
	for at, path := range paths {
		steps = append(steps, `if ! printf '%s' "$origin`+strconv.Itoa(at)+`" | base64 -d | docker run -i --user "0:$group" `+
			shell.Words(Placement(dir, "place-origin", path)[2:])+`; then exit `+strconv.Itoa(ExitPlaceFailed)+`; fi`)
	}
	return steps
}

func originUnplacing(dir string, kept []string) string {
	return `if ! ` + shell.Words(Placement(dir, append([]string{"unplace-origins"}, kept...)...)) +
		` </dev/null; then exit ` + strconv.Itoa(ExitPlaceFailed) + `; fi`
}

func (t *Table) writePair(ctx context.Context, expected Digest, pair Pair, placing bool) (Digest, error, error) {
	file := t.front.File()
	if file != boxapi.ProxyConfig && !placing {
		file = ""
	}
	elevation := ssh.RootWhenAble
	result, err := t.box.Stream(ctx, elevation, StagedWrite(expected, file, OriginPaths(pair.Origins)), strings.NewReader(PairFed(pair)))
	if err != nil {
		return "", nil, err
	}
	switch result.Code {
	case 0:
		return Digest(strings.TrimSpace(result.Stdout)), nil, nil
	case ExitPlaceFailed:
		return Digest(strings.TrimSpace(result.Stdout)), t.box.Refuse("place "+file, result), nil
	case ExitMoved:
		return "", nil, t.MovedUnder(expected, result)
	case ExitUnseeded:
		seeded := boxapi.RoutingTablePath
		if file == boxapi.ProxyConfig {
			seeded += " or " + boxapi.ProxyConfig
		}
		return "", nil, refusal.Refuse(refusal.CodeNotReady,
			"%s is missing on %s; nothing was written\nRun `ocel bootstrap` for this box's tier",
			seeded, t.box.Named())
	default:
		return "", nil, t.box.Refuse("write "+Files(file), result)
	}
}

func Files(file string) string {
	if file == "" {
		return boxapi.RoutingTablePath
	}
	return boxapi.RoutingTablePath + " and " + file
}

func (t *Table) MovedUnder(expected Digest, result ssh.Result) error {
	return refusal.Refuse(refusal.CodeBusy,
		"%s on %s changed during this deploy (%s, expected %s); nothing was written\nRun the deploy again",
		boxapi.RoutingTablePath, t.box.Named(), strings.TrimSpace(result.Stderr), expected)
}

type stagedFile struct{ at, staged, fed string }

func StagedWrite(expected Digest, file string, origins []string) string {
	files := []stagedFile{{at: boxapi.RoutingTablePath, staged: "staged", fed: "written"}}
	if file == boxapi.ProxyConfig {
		files = append(files, stagedFile{at: boxapi.ProxyConfig, staged: "rendered", fed: "rendering"})
	}
	var staging, taken, decoding, unseeded, owning, moving, reading, placing []string
	for _, file := range files {
		at, staged := shell.Quote(file.at), `"$`+file.staged+`"`
		staging = append(staging, file.staged+`=$(mktemp `+shell.Quote(file.at+".XXXXXX")+`)`)
		taken = append(taken, staged)
		decoding = append(decoding, `printf '%s' "$`+file.fed+`" | base64 -d > `+staged)
		unseeded = append(unseeded, `[ ! -f `+at+` ]`)
		owning = append(owning, `chmod --reference=`+at+` `+staged, `chown --reference=`+at+` `+staged)
		moving = append(moving, `mv `+staged+` `+at)
	}
	if file != "" && file != boxapi.ProxyConfig {
		reading = originReading(origins)
		placing = slices.Concat(originPlacing(origins), []string{placeStep(`printf '%s' "$rendering" | base64 -d | `, file), originUnplacing(filepath.Dir(file), origins)})
	}
	return strings.Join(slices.Concat(
		[]string{"set -e"},
		staging,
		[]string{`trap 'rm -f ` + strings.Join(taken, " ") + `' EXIT`, `IFS= read -r written`, `IFS= read -r rendering`},
		reading,
		decoding,
		[]string{
			strings.TrimSuffix(Locked("-x"), "\n"),
			`if ` + strings.Join(unseeded, " || ") + `; then exit ` + strconv.Itoa(ExitUnseeded) + `; fi`,
		},
		ComparedUnder(expected),
		owning,
		[]string{`sha256sum "$staged" | cut -d' ' -f1`},
		moving,
		placing,
		[]string{"trap - EXIT"},
	), "\n")
}

func ComparedUnder(expected Digest) []string {
	return []string{
		`current=$(sha256sum ` + shell.Quote(boxapi.RoutingTablePath) + ` | cut -d' ' -f1)`,
		`if [ "$current" != ` + shell.Quote(string(expected)) + ` ]; then printf '%s' "$current" >&2; exit ` + strconv.Itoa(ExitMoved) + `; fi`,
	}
}

func placeStep(feed, at string) string {
	return `if ! ` + feed + shell.Words(PlacementFed(filepath.Dir(at), "place", at)) + `; then exit ` + strconv.Itoa(ExitPlaceFailed) + `; fi`
}

func Replacement(expected Digest, at string, origins []string) string {
	return strings.Join(slices.Concat(
		[]string{"set -e"},
		originReading(origins),
		[]string{strings.TrimSuffix(Locked("-x"), "\n")},
		ComparedUnder(expected),
		originPlacing(origins),
		[]string{placeStep("", at), originUnplacing(filepath.Dir(at), origins)},
	), "\n")
}

func (t *Table) DestinationSum(ctx context.Context, at string) (string, error) {
	elevation, err := ssh.Docker, t.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return "", err
	}
	said, err := t.box.Run(ctx, elevation, "read "+at, shell.Words(Placement(filepath.Dir(at), "placed", at)), nil)
	return strings.TrimSpace(said), err
}

func (t *Table) replace(ctx context.Context, expected Digest, at string, rendering []byte, origins []proxy.OriginFile) error {
	elevation := ssh.RootWhenAble
	result, err := t.box.Stream(ctx, elevation, Replacement(expected, at, OriginPaths(origins)), strings.NewReader(OriginsFed(origins)+string(rendering)))
	if err != nil {
		return err
	}
	switch result.Code {
	case 0:
		return nil
	case ExitMoved:
		return t.MovedUnder(expected, result)
	default:
		return t.box.Refuse("place "+at, result)
	}
}

func Moved(err error) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == refusal.CodeBusy
}
