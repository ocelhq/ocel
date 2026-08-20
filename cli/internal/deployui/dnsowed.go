package deployui

import (
	"fmt"
	"strings"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const (
	dnsIndent  = "  "
	dnsGutter  = "  "
	proxiedOn  = "on"
	proxiedOff = "off"
)

type dnsColumn struct {
	head string
	cell func(*progressv1.DnsRecord) string
}

func dnsColumns(records []*progressv1.DnsRecord) []dnsColumn {
	cols := []dnsColumn{
		{"TYPE", func(r *progressv1.DnsRecord) string { return r.GetType() }},
		{"NAME", func(r *progressv1.DnsRecord) string { return r.GetName() }},
		{"VALUE", func(r *progressv1.DnsRecord) string { return r.GetValue() }},
	}
	if anyProxied(records) {
		cols = append(cols, dnsColumn{"PROXY", func(r *progressv1.DnsRecord) string {
			if r.GetProxied() {
				return proxiedOn
			}
			return proxiedOff
		}})
	}
	return cols
}

func anyProxied(records []*progressv1.DnsRecord) bool {
	for _, rec := range records {
		if rec.GetProxied() {
			return true
		}
	}
	return false
}

func dnsHeadline(headline string, records []*progressv1.DnsRecord) string {
	if len(records) == 1 {
		return fmt.Sprintf("%s — add this record at your DNS provider", headline)
	}
	return fmt.Sprintf("%s — add these %d records at your DNS provider", headline, len(records))
}

func dnsRows(records []*progressv1.DnsRecord, width int) (string, []string) {
	cols := dnsColumns(records)
	widths := make([]int, len(cols))
	for i, col := range cols {
		widths[i] = len(col.head)
		for _, rec := range records {
			widths[i] = max(widths[i], len(col.cell(rec)))
		}
	}
	if len(dnsIndent)+sum(widths)+len(dnsGutter)*(len(cols)-1) > width {
		return "", nil
	}
	cells := make([]string, len(cols))
	for i, col := range cols {
		cells[i] = col.head
	}
	head := dnsLine(cells, widths)
	rows := make([]string, 0, len(records))
	for _, rec := range records {
		for i, col := range cols {
			cells[i] = col.cell(rec)
		}
		rows = append(rows, dnsLine(cells, widths))
	}
	return head, rows
}

func dnsLine(cells []string, widths []int) string {
	var b strings.Builder
	b.WriteString(dnsIndent)
	for i, cell := range cells {
		if i > 0 {
			b.WriteString(dnsGutter)
		}
		if i == len(cells)-1 {
			b.WriteString(cell)
			break
		}
		b.WriteString(cell + strings.Repeat(" ", widths[i]-len(cell)))
	}
	return b.String()
}

func dnsStack(records []*progressv1.DnsRecord) []string {
	cols := dnsColumns(records)
	var lines []string
	for i, rec := range records {
		if i > 0 {
			lines = append(lines, "")
		}
		for _, col := range cols {
			lines = append(lines, fmt.Sprintf("%s%-6s %s", dnsIndent, title(col.head), col.cell(rec)))
		}
	}
	return lines
}

func dnsNotes(records []*progressv1.DnsRecord, notes []string) []string {
	if !anyProxied(records) {
		return notes
	}
	return append([]string{"Turn the proxy (orange cloud) on: the value is a placeholder the proxy answers behind."}, notes...)
}

func title(head string) string {
	return head[:1] + strings.ToLower(head[1:])
}

func sum(ns []int) int {
	total := 0
	for _, n := range ns {
		total += n
	}
	return total
}
