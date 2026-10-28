package providerprocess

import (
	"context"
	"fmt"
	"os"
)

const fakeCLIEnvVar = "OCEL_TEST_PROVIDER_PROCESS_CLI"

func runFakeCLI() int {
	env := append(os.Environ(), fakeProviderEnvVar+"=1", fakeProviderModeEnvVar+"=serves")
	r, err := Spawn(context.Background(), LaunchSpec{BinaryPath: os.Args[0], Env: env, Stderr: os.Stderr})
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake CLI: spawn:", err)
		return 1
	}
	if err := r.Ready(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "fake CLI: ready:", err)
		return 1
	}
	fmt.Println(r.cmd.Process.Pid)
	select {}
}
