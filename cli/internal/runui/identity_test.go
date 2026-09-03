package runui

import (
	"strings"
	"testing"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestIdentityBlock(t *testing.T) {
	t.Parallel()

	awsAndCloudflare := &streamv1.IdentityEvent{
		Project: "acme",
		Tier:    environmentv1.Tier_TIER_PRODUCTION,
		Origin:  &streamv1.Party{Vendor: "aws", Account: "123456789012", Principal: "deploy", Location: "us-east-1"},
		Edge:    &streamv1.Party{Vendor: "cloudflare", Account: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"},
	}
	vps := &streamv1.IdentityEvent{
		Project: "acme",
		Tier:    environmentv1.Tier_TIER_PREVIEW,
		Origin:  &streamv1.Party{Vendor: "vps", Account: "srv1.example.com", Principal: "deploy"},
	}

	for _, tc := range []struct {
		name string
		ev   *streamv1.IdentityEvent
		want string
	}{
		{
			name: "an origin and an edge, each account against its vendor",
			ev:   awsAndCloudflare,
			want: "▎ ocel  acme › production\n" +
				"▎ aws         123456789012  deploy  us-east-1\n" +
				"▎ cloudflare  a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4\n",
		},
		{
			name: "a host with no region reads as principal at host, and no edge line",
			ev:   vps,
			want: "▎ ocel  acme › preview\n" +
				"▎ vps   deploy@srv1.example.com\n",
		},
		{
			name: "an unnamed project leaves the tier standing alone",
			ev: &streamv1.IdentityEvent{
				Tier:   environmentv1.Tier_TIER_PRODUCTION,
				Origin: &streamv1.Party{Vendor: "aws", Account: "123456789012", Location: "us-east-1"},
			},
			want: "▎ ocel  production\n" +
				"▎ aws   123456789012  us-east-1\n",
		},
		{
			name: "an empty identity says nothing at all",
			ev:   &streamv1.IdentityEvent{},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := strings.Join(IdentityBlock(Presentation{}, tc.ev), "\n")
			if got != tc.want {
				t.Errorf("IdentityBlock() =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}

	t.Run("colour paints the rule and the name, and nothing else", func(t *testing.T) {
		t.Parallel()

		lines := IdentityBlock(Presentation{Color: true}, awsAndCloudflare)
		painted := strings.Join(lines, "\n")
		if !strings.Contains(painted, "\x1b[") {
			t.Fatalf("coloured block carries no escapes:\n%q", painted)
		}
		for _, want := range []string{"123456789012", "deploy", "us-east-1", "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"} {
			if !strings.Contains(painted, want) {
				t.Errorf("coloured block lost %q:\n%q", want, painted)
			}
		}
		if len(lines) != len(IdentityBlock(Presentation{}, awsAndCloudflare)) {
			t.Error("colour changed how many lines the block has")
		}
	})

	t.Run("no colour is the same text without a single escape", func(t *testing.T) {
		t.Parallel()

		plain := strings.Join(IdentityBlock(Presentation{}, awsAndCloudflare), "\n")
		if strings.Contains(plain, "\x1b") {
			t.Errorf("uncoloured block carries escapes:\n%q", plain)
		}
	})
}
