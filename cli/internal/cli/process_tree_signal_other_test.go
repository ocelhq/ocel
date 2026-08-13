//go:build !unix

package cli

const procTreeModeEnvVar = "OCEL_TEST_PROCTREE_MODE"

func runProcessTreeSubprocess() int { return 2 }
