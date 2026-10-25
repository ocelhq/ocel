package cloud

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
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
)

func RefusalOf(apiURL string, err *connect.Error) *clierror.Error {
	if unserved, ok := detailOf[*cloudv1.VersionUnserved](err); ok {
		return &clierror.Error{Code: clierror.CodeCloudVersionUnserved, Cause: wireerror.Error{Message: describeUnserved(apiURL, unserved.GetServed()), Wire: err}}
	}
	if err.Code() == connect.CodeUnauthenticated {
		return &clierror.Error{Code: clierror.CodeCloudTokenRefused, Hint: "ocel login", Cause: wireerror.Error{Message: fmt.Sprintf("Ocel Cloud at %s refuses the token the console issued (%s): run `ocel login`", apiURL, err.Message()), Wire: err}}
	}
	return nil
}

func IsSessionEnded(err error) bool {
	var wire *connect.Error
	if !errors.As(err, &wire) {
		return false
	}
	_, ended := detailOf[*cloudv1.SessionEnded](wire)
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

func describeUnserved(apiURL string, served []string) string {
	running := version.Version
	if len(served) == 0 {
		return fmt.Sprintf("Ocel Cloud at %s serves no release of ocel, so not ocel %s: try again later", apiURL, running)
	}
	bySemver := func(a, b string) int { return semver.Compare(semverOf(a), semverOf(b)) }
	newest := slices.MaxFunc(served, bySemver)
	if semver.IsValid(semverOf(running)) && bySemver(running, slices.MinFunc(served, bySemver)) < 0 {
		return fmt.Sprintf("ocel %s is older than every release Ocel Cloud at %s serves: upgrade ocel to %s", running, apiURL, newest)
	}
	return fmt.Sprintf("Ocel Cloud at %s serves ocel %s, not ocel %s: run this with ocel %s, or again once Ocel Cloud serves ocel %s", apiURL, strings.Join(served, ", "), running, newest, running)
}

func semverOf(v string) string {
	return "v" + strings.TrimPrefix(v, "v")
}
