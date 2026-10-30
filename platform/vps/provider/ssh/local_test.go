package ssh

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

var onBox = Local{Hostname: "box.example.com", User: boxapi.DeployLogin, Path: "/usr/bin:/bin"}

func TestALocalCommandRunsOnThisMachineAndAnswersItsOutput(t *testing.T) {
	t.Parallel()

	said, err := onBox.Run(context.Background(), "printf '%s' \"$PATH\"")
	if err != nil || said != "/usr/bin:/bin" {
		t.Fatalf("Run = %q, %v, want the PATH the box's commands run with", said, err)
	}
	result, err := onBox.Stream(context.Background(), "cat; exit 3", strings.NewReader("fed"))
	if err != nil || result.Stdout != "fed" || result.Code != 3 {
		t.Errorf("Stream = %+v, %v, want stdin echoed and the exit kept", result, err)
	}
	if _, err := onBox.Run(context.Background(), "echo nope >&2; exit 1"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("Run of a failing command = %v, want what it said", err)
	}
}

func TestALocalCommandStreamsEachLineAsItIsWritten(t *testing.T) {
	t.Parallel()

	var lines []Line
	err := onBox.RunLines(context.Background(), "echo one; echo two >&2; echo three", func(line Line) error {
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 0, len(lines))
	for _, line := range lines {
		texts = append(texts, line.Text)
	}
	slices.Sort(texts)
	if !slices.Equal(texts, []string{"one", "three", "two"}) {
		t.Errorf("lines = %+v", lines)
	}
}

func TestABoxDrivenLocallyAsTheDeployLoginRunsDeployCommandsWithoutSudo(t *testing.T) {
	t.Parallel()

	box := NewBox(func(context.Context) (Conn, error) { return onBox, nil }, nil)
	said, err := box.Run(context.Background(), Deploy, "ask who runs this", "echo ran", nil)
	if err != nil || strings.TrimSpace(said) != "ran" {
		t.Fatalf("Run as the deploy login = %q, %v", said, err)
	}
	if box.Named() != boxapi.DeployLogin+"@box.example.com" {
		t.Errorf("Named() = %q", box.Named())
	}
}

func TestAPortOnTheBoxIsReachedWhereItIs(t *testing.T) {
	t.Parallel()

	at, closing, err := onBox.ForwardPort(context.Background(), "172.18.0.4:5432")
	if err != nil || at != "172.18.0.4:5432" {
		t.Fatalf("ForwardPort = %q, %v, want the address itself", at, err)
	}
	closing()
}
