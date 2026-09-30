package terminal

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSelectTakesTheOptionItsNumberNames(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	options := []Option{{Name: "aws"}, {Name: "gcp"}, {Name: "vps"}}
	chosen, answered, err := NewPrompt(&out, strings.NewReader("7\n3\n")).Select(context.Background(), "Where should shop deploy?", options)
	if err != nil || !answered {
		t.Fatalf("Select() = %q, %t, %v; want an answer", chosen, answered, err)
	}
	if chosen != "vps" {
		t.Errorf("Select() = %q, want vps: 7 is no option, so it asks again", chosen)
	}
	if !strings.Contains(out.String(), "Where should shop deploy?") || !strings.Contains(out.String(), "3) vps") {
		t.Errorf("asked %q, want the title and the numbered options", out.String())
	}
}

func TestSelectWithNothingToReadIsUnanswered(t *testing.T) {
	t.Parallel()

	_, answered, err := NewPrompt(&bytes.Buffer{}, strings.NewReader("")).Select(context.Background(), "Pick", []Option{{Name: "aws"}})
	if err != nil || answered {
		t.Errorf("Select() = %t, %v; want it unanswered", answered, err)
	}
}

func TestInputTakesTheLineTyped(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	typed, answered, err := NewPrompt(&out, strings.NewReader("  203.0.113.7 \n")).Input(context.Background(), "ssh", "The machine to deploy onto")
	if err != nil || !answered || typed != "203.0.113.7" {
		t.Errorf("Input() = %q, %t, %v; want the address typed", typed, answered, err)
	}
	if !strings.Contains(out.String(), "The machine to deploy onto") {
		t.Errorf("asked %q, want the description shown", out.String())
	}
}

func TestAwaitEnterGoesOnAtEnterAndStopsAtNo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		typed string
		want  bool
	}{
		{"\n", true},
		{"y\n", true},
		{"n\n", false},
		{"", false},
	} {
		var out bytes.Buffer
		got, err := NewPrompt(&out, strings.NewReader(tc.typed)).AwaitEnter(context.Background(), "Press Enter once it's saved (or n to stop)")
		if err != nil || got != tc.want {
			t.Errorf("AwaitEnter(%q) = %t, %v; want %t", tc.typed, got, err, tc.want)
		}
	}
}
