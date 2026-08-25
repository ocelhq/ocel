//go:build !unix

package cli

const procTreeModeEnvVar = "OCEL_TEST_PROCTREE_MODE"

const procTreeSessionHarnessEnvVar = "OCEL_TEST_PROCTREE_SESSION_HARNESS"

func runProcessTreeSubprocess() int { return 2 }

func runProcessTreeSessionHarness() int { return 2 }
