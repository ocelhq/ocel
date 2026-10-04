package terminal

import (
	"os"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

// Throwaway (research/proto-jsonschema): writes NDJSON lines through the real
// JSONLines sink for a populated summary and a nil-operation event.
func TestResearchWritesPopulatedSummarySample(t *testing.T) {
	path := os.Getenv("OCEL_RESEARCH_SAMPLE_OUT")
	if path == "" {
		t.Skip("set OCEL_RESEARCH_SAMPLE_OUT")
	}
	var out safeBuffer
	sink := NewJSONLines(&out)
	at := time.Date(2026, 10, 4, 17, 9, 51, 39813795, time.UTC)
	sink.Receive(&streamv1.RunEvent{
		Operation: &progressv1.OperationEvent{
			Time:   timestamppb.New(at),
			Level:  progressv1.Level_LEVEL_INFO,
			Phase:  progressv1.Phase_PHASE_DEPLOY,
			SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8},
			Body: &progressv1.OperationEvent_Ended{Ended: &progressv1.Ended{
				Status:            progressv1.SpanStatus_SPAN_STATUS_OK,
				StartTimeUnixNano: at.Add(-3 * time.Second).UnixNano(),
				Attributes: []*progressv1.SpanAttribute{
					{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_APP, Value: "web"},
				},
				Title: "Deploy web",
			}},
		},
	})
	sink.Receive(&streamv1.RunEvent{
		Operation: &progressv1.OperationEvent{Time: timestamppb.New(at), Level: progressv1.Level_LEVEL_INFO},
		Cli: &streamv1.RunEvent_Summary{Summary: &streamv1.RunSummary{
			Success:     true,
			DurationMs:  9007199254740993,
			LogPath:     "/tmp/app/.ocel/runs/c1c62fc966c1a26541cf11184e5610bd.ndjson",
			Headline:    "Deployed to production",
			UrlNotes:    []string{"https://app.example.com may take a few minutes to resolve"},
			Propagation: &progressv1.Propagation{TypicalMs: 120000, Published: true},
			Apps: []*progressv1.AppResult{{
				App:           "web",
				Outcome:       progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED,
				Urls:          []string{"https://app.example.com"},
				DeploymentUrl: "https://web-abc123.example.net",
			}},
			ChangeStarted: true,
			Tier:          environmentv1.Tier_TIER_PRODUCTION,
			Origin:        &streamv1.Party{Vendor: "aws", Account: "123456789012", Principal: "arn:aws:iam::123456789012:role/deployer", Location: "eu-west-1"},
			PromotionId:   "promo-2",
		}},
	})
	sink.Receive(&streamv1.RunEvent{Cli: &streamv1.RunEvent_Resumed{Resumed: &streamv1.ResumedEvent{Reason: "nil operation"}}})
	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
