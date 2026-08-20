package deployui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

var validation = &progressv1.DnsRecord{
	Name:  "_b833967e837b1a20c09460dc97096c55.prev.ocel.site",
	Type:  "CNAME",
	Value: "_5e03e7236ee0371f573dc3210d17afd9.jkddzztszm.acm-validations.aws",
}

var wildcard = &progressv1.DnsRecord{
	Name:  "*.prev.ocel.site",
	Type:  "CNAME",
	Value: "d1234.cloudfront.net",
}

func TestDNSRows(t *testing.T) {
	t.Parallel()

	t.Run("columns line up under their headers", func(t *testing.T) {
		t.Parallel()

		head, rows := dnsRows([]*progressv1.DnsRecord{wildcard, validation}, 200)
		if head == "" {
			t.Fatal("head = empty, want a header row at a width both records fit in")
		}
		if len(rows) != 2 {
			t.Fatalf("rows = %d, want one per record", len(rows))
		}
		for _, row := range rows {
			for _, column := range []string{"TYPE", "NAME", "VALUE"} {
				if strings.Index(head, column) >= len(row) {
					t.Fatalf("row %q is shorter than the %s column starts in %q", row, column, head)
				}
			}
			if at := strings.Index(head, "NAME"); !strings.HasPrefix(row[at:], "*") && !strings.HasPrefix(row[at:], "_") {
				t.Errorf("row = %q, want the name to start under NAME at column %d", row, at)
			}
		}
	})

	t.Run("a record too wide for the terminal gets no table", func(t *testing.T) {
		t.Parallel()

		if head, rows := dnsRows([]*progressv1.DnsRecord{validation}, 80); head != "" || rows != nil {
			t.Errorf("head = %q rows = %v, want no table: the row does not fit 80 columns", head, rows)
		}
	})

	t.Run("proxied records carry a column of their own", func(t *testing.T) {
		t.Parallel()

		proxied := &progressv1.DnsRecord{Name: "shop.app.com", Type: "AAAA", Value: "100::", Proxied: true}
		head, rows := dnsRows([]*progressv1.DnsRecord{proxied}, 200)
		if !strings.Contains(head, "PROXY") {
			t.Errorf("head = %q, want the proxy column named", head)
		}
		if !strings.HasSuffix(rows[0], proxiedOn) {
			t.Errorf("row = %q, want the proxy shown as %q", rows[0], proxiedOn)
		}
	})
}

func TestDNSStack(t *testing.T) {
	t.Parallel()

	lines := dnsStack([]*progressv1.DnsRecord{wildcard, validation})
	if len(lines) != 7 {
		t.Fatalf("lines = %d (%v), want three per record and a blank between them", len(lines), lines)
	}
	if lines[3] != "" {
		t.Errorf("lines[3] = %q, want the records separated by a blank line", lines[3])
	}
	for i, want := range []string{"Type", "Name", "Value"} {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), want) {
			t.Errorf("lines[%d] = %q, want it labelled %q", i, lines[i], want)
		}
	}
	if !strings.HasSuffix(lines[2], wildcard.GetValue()) {
		t.Errorf("lines[2] = %q, want the value typed out in full", lines[2])
	}
}

func TestRendererDNSOwed(t *testing.T) {
	t.Parallel()

	t.Run("names what the records are for and prints every field", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		r := newRendererForTest(&out, FormatHuman, false, false)
		t.Cleanup(func() { _ = r.Close() })

		r.DNSOwed("Prove you own prev.ocel.site", []*progressv1.DnsRecord{validation}, []string{"Leave it in place."})

		got := out.String()
		for _, want := range []string{
			"Prove you own prev.ocel.site",
			"add this record",
			validation.GetName(),
			validation.GetType(),
			validation.GetValue(),
			"Leave it in place.",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("output = %q, want it to contain %q", got, want)
			}
		}
	})

	t.Run("counts the records when there is more than one", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		r := newRendererForTest(&out, FormatHuman, false, false)
		t.Cleanup(func() { _ = r.Close() })

		r.DNSOwed("Point *.prev.ocel.site at the edge", []*progressv1.DnsRecord{validation, wildcard}, nil)

		if got := out.String(); !strings.Contains(got, "add these 2 records") {
			t.Errorf("output = %q, want the count named", got)
		}
	})

	t.Run("a proxied record says the value is a placeholder", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		r := newRendererForTest(&out, FormatHuman, false, false)
		t.Cleanup(func() { _ = r.Close() })

		proxied := &progressv1.DnsRecord{Name: "shop.app.com", Type: "AAAA", Value: "100::", Proxied: true}
		r.DNSOwed("Point shop.app.com at the edge", []*progressv1.DnsRecord{proxied}, nil)

		if got := out.String(); !strings.Contains(got, "orange cloud") {
			t.Errorf("output = %q, want the proxy toggle spelled out", got)
		}
	})

	t.Run("nothing owed prints nothing", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		r := newRendererForTest(&out, FormatHuman, false, false)
		t.Cleanup(func() { _ = r.Close() })

		r.DNSOwed("Prove you own prev.ocel.site", nil, []string{"Leave it in place."})

		if got := out.String(); got != "" {
			t.Errorf("output = %q, want nothing printed with no record to add", got)
		}
	})

	t.Run("json carries the records as fields, not prose", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		r := newRendererForTest(&out, FormatJSON, false, false)
		t.Cleanup(func() { _ = r.Close() })

		r.DNSOwed("Prove you own prev.ocel.site", []*progressv1.DnsRecord{validation}, nil)

		var event struct {
			Kind     string `json:"type"`
			Headline string `json:"headline"`
			Records  []struct {
				Name  string `json:"name"`
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"records"`
		}
		if err := json.Unmarshal(out.Bytes(), &event); err != nil {
			t.Fatalf("unmarshal %q: %v", out.String(), err)
		}
		if event.Kind != "dnsOwed" || len(event.Records) != 1 {
			t.Fatalf("event = %+v, want one owed record", event)
		}
		if event.Records[0].Name != validation.GetName() || event.Records[0].Value != validation.GetValue() {
			t.Errorf("record = %+v, want the fields carried through", event.Records[0])
		}
	})
}
