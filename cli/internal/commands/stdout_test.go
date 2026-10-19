package commands_test

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
)

func groupWithAListAndARun() (list, run *cobra.Command, stdout, stderr *bytes.Buffer) {
	group := commands.ReserveStdout(&cobra.Command{Use: "projects"})
	list = &cobra.Command{Use: "ls"}
	run = commands.DeclareRunEvents(&cobra.Command{Use: "destroy"})
	group.AddCommand(list, run)
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	group.SetOut(stdout)
	group.SetErr(stderr)
	return list, run, stdout, stderr
}

func TestACommandUnderAGroupThatReservesStdoutKeepsStdoutForItsData(t *testing.T) {
	list, _, _, stderr := groupWithAListAndARun()

	if commands.ChooseRunOutput(list) != stderr || !commands.PrintsData(list) {
		t.Error("ls under a group that reserves stdout draws its run on stdout, want stdout kept for its data")
	}
}

func TestARunCommandUnderAGroupThatReservesStdoutDrawsItsRunOnStdout(t *testing.T) {
	_, run, stdout, _ := groupWithAListAndARun()

	if commands.ChooseRunOutput(run) != stdout || commands.PrintsData(run) {
		t.Error("destroy declares run events under a group that reserves stdout, want its run drawn on stdout")
	}
}
