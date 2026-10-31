package endpointcall

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"golang.org/x/mod/semver"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/cli/internal/wireerror"
	endpointv1 "github.com/ocelhq/ocel/pkg/proto/endpoint/v1"
)

type Caller int

const (
	CloudSession Caller = iota
	Runner
)

func RefuseUnserved(name, apiURL string, served []string) error {
	return &clierror.Error{Code: clierror.CodeCloudVersionUnserved, Cause: errors.New(DescribeUnserved(name, apiURL, served))}
}

func RefusalOf(caller Caller, name, apiURL string, err *connect.Error) *clierror.Error {
	if unserved, ok := detailOf[*endpointv1.VersionUnserved](err); ok {
		cause := wireerror.Error{Message: DescribeUnserved(name, apiURL, unserved.GetServed()), Wire: err}
		if caller == Runner {
			return &clierror.Error{Code: clierror.CodeJobVersionUnserved, Cause: cause}
		}
		return &clierror.Error{Code: clierror.CodeCloudVersionUnserved, Cause: cause}
	}
	if err.Code() == connect.CodeUnauthenticated {
		cause := wireerror.Error{Message: fmt.Sprintf("%s at %s refuses the token the console issued (%s): run `ocel login` and run this again", name, apiURL, err.Message()), Wire: err}
		if caller == Runner {
			return &clierror.Error{Code: clierror.CodeJobTokenRefused, Hint: "ocel login", Cause: cause}
		}
		return &clierror.Error{Code: clierror.CodeCloudTokenRefused, Hint: "ocel login", Cause: cause}
	}
	return nil
}

func IsSessionEnded(err error) bool {
	var wire *connect.Error
	if !errors.As(err, &wire) {
		return false
	}
	_, ended := detailOf[*endpointv1.SessionEnded](wire)
	return ended
}

func detailOf[T any](err *connect.Error) (T, bool) {
	for _, detail := range err.Details() {
		value, decodeErr := detail.Value()
		if decodeErr != nil {
			continue
		}
		if typed, ok := value.(T); ok {
			return typed, true
		}
	}
	var none T
	return none, false
}

func DescribeUnserved(name, apiURL string, served []string) string {
	running := version.Version
	if len(served) == 0 {
		return fmt.Sprintf("%s at %s serves no release of ocel at the moment, so not ocel %s: run this again later", name, apiURL, running)
	}
	bySemver := func(a, b string) int { return semver.Compare(semverOf(a), semverOf(b)) }
	newest := slices.MaxFunc(served, bySemver)
	if semver.IsValid(semverOf(running)) && bySemver(running, slices.MinFunc(served, bySemver)) < 0 {
		return fmt.Sprintf("ocel %s is older than every release %s at %s serves: upgrade ocel to %s and run this again", running, name, apiURL, newest)
	}
	return fmt.Sprintf("%s at %s serves ocel %s, not ocel %s: run this with ocel %s, or again once %s serves ocel %s", name, apiURL, strings.Join(served, ", "), running, newest, name, running)
}

func semverOf(v string) string {
	return "v" + strings.TrimPrefix(v, "v")
}
