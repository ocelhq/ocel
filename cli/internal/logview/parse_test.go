package logview_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/logview"
)

func TestParseReadsEachLoggingFormat(t *testing.T) {
	t.Parallel()

	utc := func(year int, month time.Month, day, hour, minute, second, nano int) time.Time {
		return time.Date(year, month, day, hour, minute, second, nano, time.UTC)
	}

	cases := []struct {
		name string
		raw  string
		want logview.Entry
	}{
		{
			name: "pino",
			raw:  `{"level":30,"time":1700000000123,"pid":4,"hostname":"web","msg":"listening","port":3000}`,
			want: logview.Entry{
				Message: "listening",
				Level:   logview.LevelInfo,
				Time:    time.UnixMilli(1700000000123),
				Fields:  map[string]any{"pid": json.Number("4"), "hostname": "web", "port": json.Number("3000")},
			},
		},
		{
			name: "pino with an error",
			raw:  `{"level":50,"time":1700000000000,"msg":"request failed","err":{"type":"Error","message":"boom","stack":"Error: boom\n    at run (app.js:1:1)"}}`,
			want: logview.Entry{
				Message: "request failed",
				Level:   logview.LevelError,
				Time:    time.UnixMilli(1700000000000),
				Error:   "Error: boom\n    at run (app.js:1:1)",
				Fields:  map[string]any{"err": map[string]any{"type": "Error", "message": "boom"}},
			},
		},
		{
			name: "bunyan",
			raw:  `{"name":"api","hostname":"web","pid":4,"level":40,"msg":"slow","time":"2024-05-01T10:00:00.250Z","v":0}`,
			want: logview.Entry{
				Message: "slow",
				Level:   logview.LevelWarn,
				Time:    utc(2024, 5, 1, 10, 0, 0, 250_000_000),
				Fields:  map[string]any{"name": "api", "hostname": "web", "pid": json.Number("4"), "v": json.Number("0")},
			},
		},
		{
			name: "winston",
			raw:  `{"level":"warn","message":"disk almost full","timestamp":"2024-05-01T10:00:00Z","used":"91%"}`,
			want: logview.Entry{
				Message: "disk almost full",
				Level:   logview.LevelWarn,
				Time:    utc(2024, 5, 1, 10, 0, 0, 0),
				Fields:  map[string]any{"used": "91%"},
			},
		},
		{
			name: "slog json",
			raw:  `{"time":"2024-05-01T10:00:00Z","level":"INFO","msg":"served","path":"/","status":200}`,
			want: logview.Entry{
				Message: "served",
				Level:   logview.LevelInfo,
				Time:    utc(2024, 5, 1, 10, 0, 0, 0),
				Fields:  map[string]any{"path": "/", "status": json.Number("200")},
			},
		},
		{
			name: "slog json with a level offset",
			raw:  `{"time":"2024-05-01T10:00:00Z","level":"ERROR+2","msg":"worse"}`,
			want: logview.Entry{
				Message: "worse",
				Level:   logview.LevelError,
				Time:    utc(2024, 5, 1, 10, 0, 0, 0),
			},
		},
		{
			name: "slog text",
			raw:  `time=2024-05-01T10:00:00.000Z level=WARN msg="cache cold" key=users`,
			want: logview.Entry{
				Message: "cache cold",
				Level:   logview.LevelWarn,
				Time:    utc(2024, 5, 1, 10, 0, 0, 0),
				Fields:  map[string]any{"key": "users"},
			},
		},
		{
			name: "zap",
			raw:  `{"level":"error","ts":1700000000.5,"caller":"app/main.go:10","msg":"query failed","error":"timeout"}`,
			want: logview.Entry{
				Message: "query failed",
				Level:   logview.LevelError,
				Time:    time.Unix(1700000000, 500_000_000),
				Error:   "timeout",
				Fields:  map[string]any{"caller": "app/main.go:10"},
			},
		},
		{
			name: "zap dpanic",
			raw:  `{"level":"dpanic","msg":"invariant broken"}`,
			want: logview.Entry{Message: "invariant broken", Level: logview.LevelError},
		},
		{
			name: "structlog",
			raw:  `{"event":"user created","level":"info","timestamp":"2024-05-01T10:00:00Z","user_id":7}`,
			want: logview.Entry{
				Message: "user created",
				Level:   logview.LevelInfo,
				Time:    utc(2024, 5, 1, 10, 0, 0, 0),
				Fields:  map[string]any{"user_id": json.Number("7")},
			},
		},
		{
			name: "structlog with an exception",
			raw:  `{"event":"charge failed","level":"error","exception":"Traceback (most recent call last):\n  File \"a.py\", line 1\nValueError: no"}`,
			want: logview.Entry{
				Message: "charge failed",
				Level:   logview.LevelError,
				Error:   "Traceback (most recent call last):\n  File \"a.py\", line 1\nValueError: no",
			},
		},
		{
			name: "rust tracing json",
			raw:  `{"timestamp":"2024-05-01T10:00:00.5Z","level":"DEBUG","fields":{"message":"tick","n":3},"target":"app::clock"}`,
			want: logview.Entry{
				Message: "tick",
				Level:   logview.LevelDebug,
				Time:    utc(2024, 5, 1, 10, 0, 0, 500_000_000),
				Fields:  map[string]any{"fields": map[string]any{"n": json.Number("3")}, "target": "app::clock"},
			},
		},
		{
			name: "zap with nanosecond precision in seconds",
			raw:  `{"ts":1700000000.123456789,"msg":"precise"}`,
			want: logview.Entry{Message: "precise", Time: time.Unix(1700000000, 123_456_789)},
		},
		{
			name: "milliseconds with a fraction",
			raw:  `{"time":1700000000123.456789,"msg":"precise"}`,
			want: logview.Entry{Message: "precise", Time: time.UnixMilli(1700000000123).Add(456_789)},
		},
		{
			name: "an epoch in nanoseconds stays a field",
			raw:  `{"time":1700000000123456789,"msg":"x"}`,
			want: logview.Entry{Message: "x", Fields: map[string]any{"time": json.Number("1700000000123456789")}},
		},
		{
			name: "an epoch in microseconds stays a field",
			raw:  `{"time":1700000000123456,"msg":"x"}`,
			want: logview.Entry{Message: "x", Fields: map[string]any{"time": json.Number("1700000000123456")}},
		},
		{
			name: "an infinite time stays a field",
			raw:  `{"ts":"Inf","msg":"x"}`,
			want: logview.Entry{Message: "x", Fields: map[string]any{"ts": "Inf"}},
		},
		{
			name: "a huge time stays a field",
			raw:  `{"ts":1e300,"msg":"x"}`,
			want: logview.Entry{Message: "x", Fields: map[string]any{"ts": json.Number("1e300")}},
		},
		{
			name: "gcp notice severity",
			raw:  `{"severity":"NOTICE","message":"deployed"}`,
			want: logview.Entry{Message: "deployed", Level: logview.LevelInfo},
		},
		{
			name: "gcp alert severity",
			raw:  `{"severity":"ALERT","message":"paged"}`,
			want: logview.Entry{Message: "paged", Level: logview.LevelError},
		},
		{
			name: "gcp emergency severity",
			raw:  `{"severity":"EMERGENCY","message":"down"}`,
			want: logview.Entry{Message: "down", Level: logview.LevelError},
		},
		{
			name: "json with severity and a critical level",
			raw:  `{"severity":"CRITICAL","message":"down"}`,
			want: logview.Entry{Message: "down", Level: logview.LevelError},
		},
		{
			name: "json with levelname and the word warning",
			raw:  `{"levelname":"WARNING","message":"careful"}`,
			want: logview.Entry{Message: "careful", Level: logview.LevelWarn},
		},
		{
			name: "json with lvl and fatal",
			raw:  `{"lvl":"fatal","msg":"dead"}`,
			want: logview.Entry{Message: "dead", Level: logview.LevelError},
		},
		{
			name: "json with panic",
			raw:  `{"level":"panic","msg":"dead"}`,
			want: logview.Entry{Message: "dead", Level: logview.LevelError},
		},
		{
			name: "json with a trace number on the pino scale",
			raw:  `{"level":10,"msg":"fine"}`,
			want: logview.Entry{Message: "fine", Level: logview.LevelDebug},
		},
		{
			name: "json with a fatal number on the pino scale",
			raw:  `{"level":60,"msg":"gone"}`,
			want: logview.Entry{Message: "gone", Level: logview.LevelError},
		},
		{
			name: "json with an unrecognised level keeps it as a field",
			raw:  `{"level":"loud","msg":"hi"}`,
			want: logview.Entry{Message: "hi", Fields: map[string]any{"level": "loud"}},
		},
		{
			name: "json with the error under error.stack",
			raw:  `{"msg":"failed","error":{"stack":"trace"}}`,
			want: logview.Entry{Message: "failed", Error: "trace"},
		},
		{
			name: "json with a top-level stack",
			raw:  `{"msg":"failed","stack":"trace"}`,
			want: logview.Entry{Message: "failed", Error: "trace"},
		},
		{
			name: "json without a message falls back to the raw line",
			raw:  `{"level":"info","a":1}`,
			want: logview.Entry{Message: `{"level":"info","a":1}`, Level: logview.LevelInfo, Fields: map[string]any{"a": json.Number("1")}},
		},
		{
			name: "json takes msg before message",
			raw:  `{"message":"second","msg":"first"}`,
			want: logview.Entry{Message: "first", Fields: map[string]any{"message": "second"}},
		},
		{
			name: "logrus text",
			raw:  `time="2024-05-01T10:00:00Z" level=info msg="Server started" port=8080`,
			want: logview.Entry{
				Message: "Server started",
				Level:   logview.LevelInfo,
				Time:    utc(2024, 5, 1, 10, 0, 0, 0),
				Fields:  map[string]any{"port": "8080"},
			},
		},
		{
			name: "logfmt with a quoted escape",
			raw:  `level=error msg="said \"no\"" err="bad thing"`,
			want: logview.Entry{Message: `said "no"`, Level: logview.LevelError, Error: "bad thing"},
		},
		{
			name: "python logging",
			raw:  `ERROR:root:something broke`,
			want: logview.Entry{Message: "something broke", Level: logview.LevelError, Fields: map[string]any{"logger": "root"}},
		},
		{
			name: "python logging with a hyphenated logger",
			raw:  `ERROR:my-app:boom`,
			want: logview.Entry{Message: "boom", Level: logview.LevelError, Fields: map[string]any{"logger": "my-app"}},
		},
		{
			name: "panic as a level prefix",
			raw:  `PANIC: invariant broken`,
			want: logview.Entry{Message: "invariant broken", Level: logview.LevelError},
		},
		{
			name: "dpanic as a bracketed level",
			raw:  `[DPANIC] invariant broken`,
			want: logview.Entry{Message: "invariant broken", Level: logview.LevelError},
		},
		{
			name: "logfmt with severity and message",
			raw:  `severity=warning message="disk almost full" used=91%`,
			want: logview.Entry{Message: "disk almost full", Level: logview.LevelWarn, Fields: map[string]any{"used": "91%"}},
		},
		{
			name: "logfmt with lvl and event",
			raw:  `lvl=info event=started`,
			want: logview.Entry{Message: "started", Level: logview.LevelInfo},
		},
		{
			name: "a sentence holding level and msg pairs is plain text",
			raw:  `set level=debug and msg=hi for now`,
			want: logview.Entry{Message: `set level=debug and msg=hi for now`},
		},
		{
			name: "logfmt with a bare word is plain text",
			raw:  `level=info msg=hi verbose`,
			want: logview.Entry{Message: `level=info msg=hi verbose`},
		},
		{
			name: "env_logger",
			raw:  `[2024-05-01T10:00:00Z ERROR my_app::db] connection lost`,
			want: logview.Entry{
				Message: "connection lost",
				Level:   logview.LevelError,
				Time:    utc(2024, 5, 1, 10, 0, 0, 0),
				Fields:  map[string]any{"target": "my_app::db"},
			},
		},
		{
			name: "bracketed level",
			raw:  `[WARN] low memory`,
			want: logview.Entry{Message: "low memory", Level: logview.LevelWarn},
		},
		{
			name: "level and colon",
			raw:  `INFO: ready`,
			want: logview.Entry{Message: "ready", Level: logview.LevelInfo},
		},
		{
			name: "level and space",
			raw:  `DEBUG cache miss`,
			want: logview.Entry{Message: "cache miss", Level: logview.LevelDebug},
		},
		{
			name: "level after a timestamp",
			raw:  `2024-05-01T10:00:00Z WARNING retrying`,
			want: logview.Entry{Message: "retrying", Level: logview.LevelWarn, Time: utc(2024, 5, 1, 10, 0, 0, 0)},
		},
		{
			name: "bracketed level after a timestamp",
			raw:  `2024-05-01T10:00:00Z [ERROR] failed`,
			want: logview.Entry{Message: "failed", Level: logview.LevelError, Time: utc(2024, 5, 1, 10, 0, 0, 0)},
		},
		{
			name: "plain text",
			raw:  `server listening on :3000`,
			want: logview.Entry{Message: `server listening on :3000`},
		},
		{
			name: "a level word inside a longer word is plain text",
			raw:  `INFORMATION is power`,
			want: logview.Entry{Message: `INFORMATION is power`},
		},
		{
			name: "a lowercase level word is plain text",
			raw:  `error: cannot find module`,
			want: logview.Entry{Message: `error: cannot find module`},
		},
		{
			name: "invalid json",
			raw:  `{"level":"info","msg":"cut off`,
			want: logview.Entry{Message: `{"level":"info","msg":"cut off`},
		},
		{
			name: "json that is not an object",
			raw:  `{not json}`,
			want: logview.Entry{Message: `{not json}`},
		},
		{
			name: "two key=value pairs without a level or msg are plain text",
			raw:  `a=1 b=2`,
			want: logview.Entry{Message: `a=1 b=2`},
		},
		{
			name: "a sentence with one pair is plain text",
			raw:  `retrying with level=3 after the failure`,
			want: logview.Entry{Message: `retrying with level=3 after the failure`},
		},
		{
			name: "empty line",
			raw:  ``,
			want: logview.Entry{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want := tc.want
			want.Raw = tc.raw
			got := logview.Parse(tc.raw)
			if !got.Time.Equal(want.Time) {
				t.Errorf("Time = %v, want %v", got.Time, want.Time)
			}
			got.Time, want.Time = time.Time{}, time.Time{}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Parse(%q)\n got  %#v\n want %#v", tc.raw, got, want)
			}
		})
	}
}

func TestParseKeepsAnUnparseableTimeAsAField(t *testing.T) {
	t.Parallel()

	got := logview.Parse(`{"msg":"x","time":"yesterday"}`)
	if !got.Time.IsZero() {
		t.Errorf("Time = %v, want zero", got.Time)
	}
	if got.Fields["time"] != "yesterday" {
		t.Errorf("Fields = %v, want time kept", got.Fields)
	}
}

func TestParseTreatsLeadingWhitespaceBeforeJSONAsJSON(t *testing.T) {
	t.Parallel()

	got := logview.Parse(`  {"level":"info","msg":"indented"}  `)
	if got.Message != "indented" || got.Level != logview.LevelInfo {
		t.Errorf("Parse = %#v", got)
	}
}
