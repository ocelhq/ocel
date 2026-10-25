package providerserver_test

import (
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestAProviderThatBuildsNoNextContainerFromAFunctionRefusesOne(t *testing.T) {
	builtProject(t)
	client := servedBy(t, fake.NewProvider(fake.Options{}))

	_, _, err := deployStream(t, client, functionContainerDeployRequest(builtServerFunction()))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "run `ocel build` again") {
		t.Fatalf("Deploy() = %v, want a refusal naming the build that makes this provider an image to run", err)
	}
}
