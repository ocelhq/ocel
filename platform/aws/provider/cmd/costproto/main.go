// PROTOTYPE — throwaway. Standalone `ocel cost` against a real account; read-only; never wired into the CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bcmdataexports"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
)

const (
	bootstrapStackPrefix = "ocel-bootstrap"
	historyStart         = "2026-06-01"
	flukeDay             = "2026-08-09"
	window               = 28
	zGate                = 5.0
	monthlyFloor         = 10.0
	pagePrice            = 0.01
)

var treeTags = []string{"ocel:project", "ocel:env", "ocel:app", "ocel:bootstrapped-by"}

type day struct {
	date  string
	total float64
	by    map[string]float64
}

type resource struct {
	stack, logical, kind, physical string
}

type session struct {
	ce      *costexplorer.Client
	cfn     *cloudformation.Client
	bcm     *bcmdataexports.Client
	sts     *sts.Client
	account string
	today   time.Time
	pages   int
	history []day
	stacks  map[string][]string
	res     map[string][]resource
}

type period struct {
	start, end time.Time
}

func (p period) String() string {
	return p.start.Format("2006-01-02") + ".." + p.end.AddDate(0, 0, -1).Format("2006-01-02")
}

func (p period) contains(date string) bool {
	d, _ := time.Parse("2006-01-02", date)
	return !d.Before(p.start) && d.Before(p.end)
}

func main() {
	ctx := context.Background()
	var positional []string
	by, per := "", "28d"
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--by" && i+1 < len(args):
			by = args[i+1]
			i++
		case args[i] == "--period" && i+1 < len(args):
			per = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--"):
			die(fmt.Errorf("unknown flag %s", args[i]))
		default:
			positional = append(positional, args[i])
		}
	}
	s, err := newSession(ctx)
	die(err)
	cmd := "account"
	if len(positional) > 0 {
		cmd = positional[0]
	}
	switch cmd {
	case "status":
		die(s.status(ctx))
	case "replay":
		die(s.replay(ctx))
	case "hold":
		die(s.hold(ctx))
	default:
		p, err := parsePeriod(per, s.today)
		die(err)
		die(s.read(ctx, cmd, by, p))
	}
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "costproto:", err)
		os.Exit(1)
	}
}

func newSession(ctx context.Context) (*session, error) {
	cfg, err := sdkconfig.Control(ctx, "")
	if err != nil {
		return nil, err
	}
	useast := func(o *costexplorer.Options) { o.Region = "us-east-1" }
	s := &session{
		ce:    costexplorer.NewFromConfig(cfg, useast),
		cfn:   cloudformation.NewFromConfig(cfg),
		bcm:   bcmdataexports.NewFromConfig(cfg, func(o *bcmdataexports.Options) { o.Region = "us-east-1" }),
		sts:   sts.NewFromConfig(cfg),
		today: time.Now().UTC().Truncate(24 * time.Hour),
	}
	id, err := s.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}
	s.account = aws.ToString(id.Account)
	return s, nil
}

func parsePeriod(spec string, today time.Time) (period, error) {
	switch {
	case spec == "mtd":
		return period{time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC), today}, nil
	case spec == "28d":
		return period{today.AddDate(0, 0, -window), today}, nil
	case strings.Contains(spec, ".."):
		a, b, _ := strings.Cut(spec, "..")
		start, err := time.Parse("2006-01-02", a)
		if err != nil {
			return period{}, err
		}
		end, err := time.Parse("2006-01-02", b)
		if err != nil {
			return period{}, err
		}
		return period{start, end.AddDate(0, 0, 1)}, nil
	case len(spec) == 7:
		start, err := time.Parse("2006-01", spec)
		if err != nil {
			return period{}, err
		}
		end := start.AddDate(0, 1, 0)
		if end.After(today) {
			end = today
		}
		return period{start, end}, nil
	default:
		start, err := time.Parse("2006-01-02", spec)
		if err != nil {
			return period{}, fmt.Errorf("--period: want mtd|YYYY-MM|YYYY-MM-DD|A..B|28d, got %q", spec)
		}
		return period{start, start.AddDate(0, 0, 1)}, nil
	}
}

func (s *session) costAndUsage(ctx context.Context, in *costexplorer.GetCostAndUsageInput) ([]cetypes.ResultByTime, error) {
	var out []cetypes.ResultByTime
	for {
		s.pages++
		resp, err := s.ce.GetCostAndUsage(ctx, in)
		if err != nil {
			return nil, err
		}
		out = append(out, resp.ResultsByTime...)
		if aws.ToString(resp.NextPageToken) == "" {
			return out, nil
		}
		in.NextPageToken = resp.NextPageToken
	}
}

func groupKey(g cetypes.Group) string {
	if len(g.Keys) == 0 {
		return ""
	}
	k := g.Keys[0]
	if i := strings.Index(k, "$"); i >= 0 {
		k = k[i+1:]
		if k == "" {
			k = "unattributed"
		}
	}
	return k
}

func amount(g map[string]cetypes.MetricValue) float64 {
	v, _ := strconv.ParseFloat(aws.ToString(g["UnblendedCost"].Amount), 64)
	return v
}

func (s *session) grouped(ctx context.Context, p period, group cetypes.GroupDefinition) ([]day, error) {
	results, err := s.costAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
		TimePeriod:  &cetypes.DateInterval{Start: aws.String(p.start.Format("2006-01-02")), End: aws.String(p.end.Format("2006-01-02"))},
		Granularity: cetypes.GranularityDaily,
		Metrics:     []string{"UnblendedCost"},
		GroupBy:     []cetypes.GroupDefinition{group},
	})
	if err != nil {
		return nil, err
	}
	var days []day
	for _, r := range results {
		d := day{date: aws.ToString(r.TimePeriod.Start), by: map[string]float64{}}
		for _, g := range r.Groups {
			v := amount(g.Metrics)
			d.by[groupKey(g)] += v
			d.total += v
		}
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].date < days[j].date })
	return days, nil
}

func (s *session) loadHistory(ctx context.Context) error {
	if s.history != nil {
		return nil
	}
	start, _ := time.Parse("2006-01-02", historyStart)
	days, err := s.grouped(ctx, period{start, s.today}, cetypes.GroupDefinition{Type: cetypes.GroupDefinitionTypeDimension, Key: aws.String("SERVICE")})
	if err != nil {
		return err
	}
	s.history = days
	return nil
}

func (s *session) loadStacks(ctx context.Context) error {
	if s.stacks != nil {
		return nil
	}
	s.stacks = map[string][]string{}
	s.res = map[string][]resource{}
	pager := cloudformation.NewDescribeStacksPaginator(s.cfn, &cloudformation.DescribeStacksInput{})
	var names []string
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, st := range page.Stacks {
			if strings.HasPrefix(aws.ToString(st.StackName), bootstrapStackPrefix) {
				names = append(names, aws.ToString(st.StackName))
			}
		}
	}
	sort.Strings(names)
	for _, name := range names {
		class := "production"
		if strings.HasSuffix(name, "-preview") {
			class = "preview"
		}
		s.stacks[class] = append(s.stacks[class], name)
		rp := cloudformation.NewListStackResourcesPaginator(s.cfn, &cloudformation.ListStackResourcesInput{StackName: aws.String(name)})
		for rp.HasMorePages() {
			page, err := rp.NextPage(ctx)
			if err != nil {
				return err
			}
			for _, r := range page.StackResourceSummaries {
				s.res[class] = append(s.res[class], resource{name, aws.ToString(r.LogicalResourceId), aws.ToString(r.ResourceType), aws.ToString(r.PhysicalResourceId)})
			}
		}
	}
	return nil
}

func (s *session) attribution() string {
	if len(s.stacks["production"])+len(s.stacks["preview"]) > 0 {
		return "managed"
	}
	return "standalone"
}

type verdict struct {
	date                    string
	cost, median, scale     float64
	z, projected            float64
	n                       int
	fire, learning, verdict bool
}

func median(xs []float64) float64 {
	c := append([]float64(nil), xs...)
	sort.Float64s(c)
	n := len(c)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

func detect(hist []float64, cost float64) verdict {
	v := verdict{cost: cost, n: len(hist)}
	if v.n < 3 {
		v.learning = true
		return v
	}
	v.median = median(hist)
	dev := make([]float64, v.n)
	maxHist := 0.0
	for i, h := range hist {
		dev[i] = math.Abs(h - v.median)
		maxHist = math.Max(maxHist, h)
	}
	v.scale = 1.4826 * median(dev)
	rel := 0.10
	if v.n < 7 {
		v.learning = true
		rel = 0.50
		v.scale = math.Max(v.scale, 0.5*maxHist)
	}
	v.scale = math.Max(v.scale, math.Max(rel*v.median, 0.01))
	v.z = (cost - v.median) / v.scale
	v.projected = (cost - v.median) * 30
	v.verdict = true
	v.fire = v.z >= zGate && v.projected >= monthlyFloor
	return v
}

func (s *session) verdicts() []verdict {
	out := make([]verdict, 0, len(s.history))
	for i, d := range s.history {
		lo := i - window
		if lo < 0 {
			lo = 0
		}
		hist := make([]float64, 0, i-lo)
		for _, h := range s.history[lo:i] {
			hist = append(hist, h.total)
		}
		v := detect(hist, d.total)
		v.date = d.date
		out = append(out, v)
	}
	return out
}

func usd(v float64) string {
	return fmt.Sprintf("$%.2f", v)
}

func (v verdict) label() string {
	switch {
	case !v.verdict:
		return fmt.Sprintf("learning, day %d/7", v.n+1)
	case v.fire:
		return "FIRE  unexplained  billed"
	case v.learning:
		return fmt.Sprintf("pass  (learning, day %d/7, wide band)", v.n+1)
	default:
		return "pass"
	}
}

func (v verdict) alertLine(scope string) string {
	return fmt.Sprintf("%s  velocity %s  %s vs %s  z %.0f  +%s/mo  unexplained  billed", v.date, scope, usd(v.cost), usd(v.median), v.z, usd(v.projected))
}

func (s *session) header(p period) {
	fmt.Printf("account %s · %s UTC · footprint read · attribution %s\n", s.account, p, s.attribution())
	fmt.Printf("Cost Explorer: %d pages (%s)\n\n", s.pages, usd(float64(s.pages)*pagePrice))
}

func (s *session) periodTotal(p period) float64 {
	t := 0.0
	for _, d := range s.history {
		if p.contains(d.date) {
			t += d.total
		}
	}
	return t
}

func (s *session) guardState(p period) {
	vs := s.verdicts()
	if len(vs) == 0 {
		fmt.Println("velocity account   no daily facts")
		return
	}
	last := vs[len(vs)-1]
	if !last.verdict {
		fmt.Printf("velocity account   %s  %s  %s\n", last.date, usd(last.cost), last.label())
	} else {
		fmt.Printf("velocity account   %s  %s  median %s  scale %s  z %.1f  %s\n", last.date, usd(last.cost), usd(last.median), usd(last.scale), last.z, last.label())
	}
	fmt.Printf("Alerts (%s)\n", p)
	fired := 0
	for _, v := range vs {
		if v.fire && p.contains(v.date) {
			fired++
			fmt.Println("  " + v.alertLine("account"))
		}
	}
	if fired == 0 {
		fmt.Println("  none")
	}
	fmt.Println("route: empty (standalone)")
}

func (s *session) read(ctx context.Context, scope, by string, p period) error {
	if err := s.loadStacks(ctx); err != nil {
		return err
	}
	if err := s.loadHistory(ctx); err != nil {
		return err
	}
	switch {
	case scope == "account" && by == "":
		s.header(p)
		s.tree(p)
	case scope == "account" || scope == "account/unattributed":
		if by == "" {
			by = "service"
		}
		rows, err := s.dimension(ctx, by, p)
		if err != nil {
			return err
		}
		s.header(p)
		s.listing(scope, by, rows, p)
	case scope == "account/platform/production" || scope == "account/platform/preview":
		s.header(p)
		s.platform(strings.TrimPrefix(scope, "account/platform/"), by)
	default:
		return fmt.Errorf("scope %q: standalone read knows account, account/platform/<production|preview>, account/unattributed", scope)
	}
	fmt.Println()
	s.guardState(p)
	return nil
}

func (s *session) tree(p period) {
	total := s.periodTotal(p)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SCOPE\tCOST\tCONF\tSHARE\t")
	fmt.Fprintf(w, "account\t%s\tbilled\t100%%\t\n", usd(total))
	for _, class := range []string{"production", "preview"} {
		note := fmt.Sprintf("%d resources known (%d stacks)", len(s.res[class]), len(s.stacks[class]))
		if len(s.stacks[class]) == 0 {
			note = "no ocel-bootstrap stack"
		} else if class == "production" {
			note += "; dollars need CUR line_item_resource_id or ocel:bootstrapped-by activation"
		}
		fmt.Fprintf(w, "  platform/%s\t—\tbilled\t—\t%s\n", class, note)
	}
	fmt.Fprintf(w, "  unattributed\t%s\tbilled\t100%%\t\n", usd(total))
	w.Flush()
	fmt.Printf("Σ children + unattributed = %s  ✓\n", usd(total))
}

type row struct {
	key  string
	cost float64
}

func (s *session) dimension(ctx context.Context, by string, p period) ([]row, error) {
	var days []day
	var err error
	switch by {
	case "service":
		for _, d := range s.history {
			if p.contains(d.date) {
				days = append(days, d)
			}
		}
	case "usage_type", "region":
		days, err = s.grouped(ctx, p, cetypes.GroupDefinition{Type: cetypes.GroupDefinitionTypeDimension, Key: aws.String(strings.ToUpper(by))})
	case "project", "env", "app":
		days, err = s.grouped(ctx, p, cetypes.GroupDefinition{Type: cetypes.GroupDefinitionTypeTag, Key: aws.String("ocel:" + by)})
	case "route":
		return nil, nil
	default:
		return nil, fmt.Errorf("--by %q: want service|usage_type|region|project|env|app|route", by)
	}
	if err != nil {
		return nil, err
	}
	sum := map[string]float64{}
	for _, d := range days {
		for k, v := range d.by {
			sum[k] += v
		}
	}
	rows := make([]row, 0, len(sum))
	for k, v := range sum {
		rows = append(rows, row{k, v})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].cost > rows[j].cost })
	return rows, nil
}

func (s *session) listing(scope, by string, rows []row, p period) {
	total := s.periodTotal(p)
	fmt.Printf("%s · %s · by %s\n", scope, p, by)
	if by == "route" {
		fmt.Println("route: empty (standalone)")
		fmt.Printf("Σ = %s  ✓ (all of it stays on %s)\n", usd(total), scope)
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\tCOST\tCONF\tSHARE\t\n", strings.ToUpper(by))
	sum := 0.0
	tagged := by == "project" || by == "env" || by == "app"
	for _, r := range rows {
		sum += r.cost
		share := "—"
		if total > 0 {
			share = fmt.Sprintf("%.0f%%", 100*r.cost/total)
		}
		note := ""
		if tagged && r.key == "unattributed" {
			note = fmt.Sprintf("tag ocel:%s is not activated for cost allocation; Cost Explorer returns every dollar under an empty tag value", by)
		}
		fmt.Fprintf(w, "%s\t%s\tbilled\t%s\t%s\n", r.key, usd(r.cost), share, note)
	}
	w.Flush()
	mark := "✓"
	if math.Abs(sum-total) > 0.005 {
		mark = fmt.Sprintf("✗ (account total for the period is %s)", usd(total))
	}
	fmt.Printf("Σ = %s  %s\n", usd(sum), mark)
}

func (s *session) platform(class, by string) {
	scope := "account/platform/" + class
	if by != "" {
		fmt.Printf("%s · by %s: no facts carry a platform join key; nothing to group\n", scope, by)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SCOPE\tCOST\tCONF\tSHARE\tRESOURCE\t")
	fmt.Fprintf(w, "%s\t—\tbilled\t—\t%d stacks\t\n", scope, len(s.stacks[class]))
	for _, r := range s.res[class] {
		fmt.Fprintf(w, "  %s/%s\t—\tbilled\t—\t%s %s\t\n", r.stack, r.logical, r.kind, r.physical)
	}
	fmt.Fprintf(w, "  unattributed\t—\tbilled\t—\t\t\n")
	w.Flush()
	fmt.Println("Σ children + unattributed = —  (0 facts carry a platform join key; needs CUR line_item_resource_id or ocel:bootstrapped-by activation)")
}

func apiError(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode() + ": " + ae.ErrorMessage()
	}
	return err.Error()
}

func (s *session) status(ctx context.Context) error {
	if err := s.loadStacks(ctx); err != nil {
		return err
	}
	dailyErr := s.loadHistory(ctx)
	yesterday := s.today.AddDate(0, 0, -1)
	s.pages++
	_, hourlyErr := s.ce.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
		TimePeriod:  &cetypes.DateInterval{Start: aws.String(yesterday.Format("2006-01-02T15:04:05Z")), End: aws.String(s.today.Format("2006-01-02T15:04:05Z"))},
		Granularity: cetypes.GranularityHourly,
		Metrics:     []string{"UnblendedCost"},
	})
	tags, tagsErr := s.ce.ListCostAllocationTags(ctx, &costexplorer.ListCostAllocationTagsInput{TagKeys: treeTags})
	exports, exportsErr := s.bcm.ListExports(ctx, &bcmdataexports.ListExportsInput{})
	monitors, monitorsErr := s.ce.GetAnomalyMonitors(ctx, &costexplorer.GetAnomalyMonitorsInput{})
	anomalies, anomaliesErr := s.ce.GetAnomalies(ctx, &costexplorer.GetAnomaliesInput{
		DateInterval: &cetypes.AnomalyDateInterval{StartDate: aws.String(flukeDay), EndDate: aws.String("2026-08-10")},
	})

	fmt.Printf("account %s · footprint read · attribution %s\n\n", s.account, s.attribution())
	if dailyErr != nil {
		fmt.Printf("✗ ce:GetCostAndUsage DAILY               %s  → CUR-only\n", apiError(dailyErr))
	} else {
		fmt.Printf("✓ ce:GetCostAndUsage DAILY               granted; %d days since %s\n", len(s.history), historyStart)
	}
	if hourlyErr != nil {
		fmt.Printf("✗ ce:GetCostAndUsage HOURLY              %s  → daily detector only\n", apiError(hourlyErr))
	} else {
		fmt.Println("✓ ce:GetCostAndUsage HOURLY              granted")
	}
	if tagsErr != nil {
		fmt.Printf("✗ ce:ListCostAllocationTags ocel:*       %s  → tree collapses to unattributed\n", apiError(tagsErr))
	} else {
		var got []string
		active := 0
		for _, t := range tags.CostAllocationTags {
			got = append(got, fmt.Sprintf("%s=%s/%s", aws.ToString(t.TagKey), t.Type, t.Status))
			if t.Status == cetypes.CostAllocationTagStatusActive {
				active++
			}
		}
		glyph := "?"
		if active == len(treeTags) {
			glyph = "✓"
		}
		fmt.Printf("%s ce:ListCostAllocationTags ocel:*       %d of %d tree tags returned, %d active: %v  → %s\n", glyph, len(got), len(treeTags), active, got, tagNote(active, len(got)))
	}
	all := append(append([]string(nil), s.stacks["production"]...), s.stacks["preview"]...)
	if len(all) == 0 {
		fmt.Println("– cloudformation:DescribeStacks          no ocel-bootstrap* stack → standalone")
	} else {
		fmt.Printf("✓ cloudformation:DescribeStacks          %d ocel-bootstrap* stacks (%d production, %d preview; %d + %d resources) → managed\n", len(all), len(s.stacks["production"]), len(s.stacks["preview"]), len(s.res["production"]), len(s.res["preview"]))
		fmt.Printf("                                         %s\n", strings.Join(all, ", "))
	}
	if exportsErr != nil {
		fmt.Printf("✗ bcm-data-exports:ListExports           %s\n", apiError(exportsErr))
	} else {
		var names []string
		ocelExport := false
		for _, e := range exports.Exports {
			n := aws.ToString(e.ExportName)
			names = append(names, n)
			ocelExport = ocelExport || strings.HasPrefix(n, "ocel-cost")
		}
		if ocelExport {
			fmt.Printf("✓ bcm-data-exports:ListExports           ocel-cost export present: %v\n", names)
		} else {
			fmt.Printf("– bcm-data-exports:ListExports           %d exports %v; no ocel-cost export; `ocel cost init` would create one\n", len(names), names)
		}
	}
	if monitorsErr != nil {
		fmt.Printf("✗ ce:GetAnomalyMonitors                  %s\n", apiError(monitorsErr))
	} else {
		var names []string
		for _, m := range monitors.AnomalyMonitors {
			names = append(names, fmt.Sprintf("%s(%s)", aws.ToString(m.MonitorName), m.MonitorType))
		}
		fmt.Printf("– ce:GetAnomalyMonitors                  %d monitors %v; no ocel monitor exists, none created\n", len(names), names)
	}
	if anomaliesErr != nil {
		fmt.Printf("✗ ce:GetAnomalies %s             %s\n", flukeDay, apiError(anomaliesErr))
	} else {
		fmt.Printf("– ce:GetAnomalies %s             %d anomalies", flukeDay, len(anomalies.Anomalies))
		for _, a := range anomalies.Anomalies {
			fmt.Printf("; %s %s..%s impact %.2f (expected %.2f, max %.2f)", aws.ToString(a.AnomalyId), aws.ToString(a.AnomalyStartDate), aws.ToString(a.AnomalyEndDate), a.Impact.TotalImpact, aws.ToFloat64(a.Impact.TotalExpectedSpend), a.Impact.MaxImpact)
		}
		fmt.Println()
	}
	fmt.Printf("\nCost Explorer: %d pages (%s)\n", s.pages, usd(float64(s.pages)*pagePrice))
	return nil
}

func tagNote(active, got int) string {
	switch {
	case active == len(treeTags):
		return "tag joins available"
	case got == 0:
		return "member sees no ocel:* keys; tree collapses to unattributed"
	default:
		return "tree collapses to unattributed until the payer activates the rest"
	}
}

func (s *session) replay(ctx context.Context) error {
	if err := s.loadHistory(ctx); err != nil {
		return err
	}
	fmt.Printf("account %s · replay %s..%s · velocity(window %d, z ≥ %.0f, projected ≥ %s/mo)\n", s.account, historyStart, s.today.AddDate(0, 0, -1).Format("2006-01-02"), window, zGate, usd(monthlyFloor))
	fmt.Printf("Cost Explorer: %d pages (%s)\n\n", s.pages, usd(float64(s.pages)*pagePrice))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  \tDATE\tCOST\tMEDIAN\tSCALE\tZ\tPROJECTED\tVERDICT\t")
	fires := 0
	for _, v := range s.verdicts() {
		mark := "  "
		if v.fire {
			mark = "▶▶"
			fires++
		}
		if !v.verdict {
			fmt.Fprintf(w, "%s\t%s\t%s\t—\t—\t—\t—\t%s\t\n", mark, v.date, usd(v.cost), v.label())
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%.1f\t%+.2f/mo\t%s\t\n", mark, v.date, usd(v.cost), usd(v.median), usd(v.scale), v.z, v.projected, v.label())
	}
	w.Flush()
	fmt.Printf("\n%d days, %d FIRE\n", len(s.history), fires)
	return nil
}

func (s *session) hold(ctx context.Context) error {
	if err := s.loadHistory(ctx); err != nil {
		return err
	}
	var last *verdict
	for _, v := range s.verdicts() {
		if v.fire {
			c := v
			last = &c
		}
	}
	fmt.Printf("account %s · SIMULATED · no Hold store exists and nothing was written; Response block is managed-only, standalone permits warn\n\n", s.account)
	if last == nil {
		fmt.Println("no velocity FIRE in the history; a Hold would have nothing to cite")
		return nil
	}
	fmt.Println("$ ocel deploy --dry   (as `runui` would render PreflightResponse.standing)")
	fmt.Println("Standing")
	fmt.Printf("  ✗ Hold on account: velocity guard fired %s — %s vs %s median, z %.0f, +%s/mo, unexplained, billed; deploys under account are blocked, teardowns are not\n", last.date, usd(last.cost), usd(last.median), last.z, usd(last.projected))
	fmt.Println("    → ocel cost hold lift account")
	fmt.Println()
	fmt.Println("StandingCheck{subject: \"account\", verdict: VERDICT_FAIL, finding: <line above>, fix: \"ocel cost hold lift account\"}")
	fmt.Printf("Alert --log-format json: {\"guard\":\"velocity\",\"scope\":\"account\",\"kind\":\"velocity\",\"response\":\"block\",\"confidence\":\"billed\",\"label\":\"unexplained\",\"z\":%.1f,\"projected\":%.2f,\"period\":\"%s\"}\n", last.z, last.projected, last.date)
	return nil
}
