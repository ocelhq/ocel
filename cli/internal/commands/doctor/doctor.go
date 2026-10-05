package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/progress"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func NewCommand(invocation commands.Invocation) *cobra.Command {
	return commands.DeclareReadOnly(commands.ReserveStdout(&cobra.Command{
		Use:   "doctor",
		Short: "Check that everything is good to go",
		Long: "Check that everything is good to go.\n\n" +
			"Reads the project, the cloud credentials it reaches with, and what production and " +
			"preview have set up, then names the fix for anything in the way. " +
			"Nothing is created or changed.",
		Example: "  $ ocel doctor",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return Run(cmd.Context(), invocation, cwd, cmd.OutOrStdout())
		},
	}))
}

func Run(ctx context.Context, invocation commands.Invocation, cwd string, stdout io.Writer) error {
	found := diagnose(ctx, invocation, cwd)
	if invocation.Presentation(stdout).Format == terminal.FormatJSON {
		if err := terminal.WriteResultJSON(stdout, found.result()); err != nil {
			return err
		}
	} else {
		found.render(stdout, terminal.PaletteFor(stdout))
	}
	if found.failures() > 0 {
		return &clierror.Error{Code: "doctor.checks_failed", Cause: &exitcode.ExitError{Code: 1}}
	}
	return nil
}

type verdict int

const (
	verdictPass verdict = iota
	verdictWarn
	verdictFail
	verdictNeutral
)

type check struct {
	verdict verdict
	text    string
	detail  []string
	fix     string
}

type section struct {
	name     string
	identity string
	checks   []check
}

type report struct {
	sections []section
}

func (s *section) pass(text string) {
	s.checks = append(s.checks, check{verdict: verdictPass, text: text})
}

func (s *section) warn(text, fix string) {
	s.checks = append(s.checks, check{verdict: verdictWarn, text: text, fix: fix})
}

func (s *section) fail(text, fix string) {
	s.checks = append(s.checks, check{verdict: verdictFail, text: text, fix: fix})
}

func (s *section) neutral(text string) {
	s.checks = append(s.checks, check{verdict: verdictNeutral, text: text})
}

func (r *report) add(sections ...section) {
	r.sections = append(r.sections, sections...)
}

func (r report) failures() int {
	return r.count(verdictFail)
}

func (r report) warnings() int {
	return r.count(verdictWarn)
}

func (r report) count(want verdict) int {
	n := 0
	for _, s := range r.sections {
		for _, c := range s.checks {
			if c.verdict == want {
				n++
			}
		}
	}
	return n
}

func checkedTiers() []environmentv1.Tier {
	return []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW}
}

func diagnose(ctx context.Context, invocation commands.Invocation, cwd string) report {
	var found report
	checked := section{name: "Project"}

	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		checked.fail(configFailure(err))
		found.add(checked)
		return found
	}

	if needed, applies := nodeCheck(ctx, cfg); applies {
		checked.checks = append(checked.checks, needed)
	}
	checked.identity = join(cfg.Slug, filepath.Base(cfg.Path))
	checked.pass(appsText(cfg))

	descriptor, providerErr := cfg.RequireProvider()
	if providerErr != nil {
		checked.fail(firstLine(providerErr.Error()), "")
	} else {
		checked.pass(providerText(descriptor))
	}
	checked.pass(edgeText(cfg))
	checked.checks = append(checked.checks, sdkChecks(cfg, version.Version)...)

	hosts := map[environmentv1.Tier][]string{}
	for _, tier := range checkedTiers() {
		hosts[tier] = cfg.HostnameNames(tier)
	}
	found.add(checked)

	if providerErr != nil {
		found.add(skippedSection("Credentials"))
		for _, tier := range checkedTiers() {
			found.add(skippedSection(title(readiness.TierName(tier))))
		}
		return found
	}

	answers := gather(ctx, invocation, cfg, descriptor.ID)
	found.add(credentialSections(cfg, answers)...)
	for _, tier := range checkedTiers() {
		found.add(tierSection(tier, hosts[tier], answers))
	}
	if checks, applies := hostCheckSection(answers); applies {
		found.add(checks)
	}
	if certificates, applies := certificateSection(answers); applies {
		found.add(certificates)
	}
	return found
}

func hostCheckSection(got *answers) (section, bool) {
	if len(got.hostChecks) == 0 {
		return section{}, false
	}
	s := section{name: "Host checks"}
	for _, check := range got.hostChecks {
		switch check.GetVerdict() {
		case contractv1.HostCheck_VERDICT_PASS:
			s.pass(check.GetFinding())
		case contractv1.HostCheck_VERDICT_NEEDS_ACTION:
			s.warn(check.GetFinding(), check.GetFix())
		default:
			s.fail(check.GetFinding(), check.GetFix())
		}
	}
	return s, true
}

func certificateSection(got *answers) (section, bool) {
	s := section{name: "Certificates"}
	for _, host := range got.hostnames {
		renewalCheck(&s, host.GetHostname(), host.GetRenewalStatus(), host.GetExpiresAt(), host.GetExpiringSoon())
	}
	if wildcard := got.wildcard; wildcard.GetBaseDomain() != "" {
		renewalCheck(&s, "*."+wildcard.GetBaseDomain(), wildcard.GetRenewalStatus(), wildcard.GetExpiresAt(), wildcard.GetExpiringSoon())
	}
	return s, len(s.checks) > 0
}

func renewalCheck(s *section, hostname, renewal string, expiresAt int64, soon bool) {
	if renewal == "" && expiresAt == 0 {
		return
	}
	text := hostname + " — " + renewalText(renewal, expiresAt)
	if soon {
		s.warn(text+" — EXPIRING SOON", "replace it before it expires; nothing here renews a certificate you pinned")
		return
	}
	s.pass(text)
}

func renewalText(renewal string, expiresAt int64) string {
	said := "no expiry reported"
	if expiresAt != 0 {
		said = "expires " + time.Unix(expiresAt, 0).UTC().Format(time.RFC3339)
	}
	if renewal == "" {
		return said
	}
	return said + ", " + renewal
}

func skippedSection(name string) section {
	s := section{name: name}
	s.neutral("skipped — no provider to check with")
	return s
}

func configFailure(err error) (string, string) {
	message := firstLine(err.Error())
	var missing project.NoConfigError
	if errors.As(err, &missing) {
		return "no " + english.Or(missing.Names) + " found in this directory or any parent",
			"run `ocel init` to set up this project"
	}
	if head, hint, ok := splitHint(message); ok {
		return head, hint
	}
	return message, ""
}

func splitHint(message string) (string, string, bool) {
	for _, sep := range []string{" — ", "; "} {
		head, hint, found := strings.Cut(message, sep)
		if found && strings.HasPrefix(hint, "run ") {
			return head, hint, true
		}
	}
	return message, "", false
}

func appsText(cfg *project.Project) string {
	if len(cfg.Apps) == 0 {
		return "config loads — no apps declared"
	}
	names := make([]string, 0, len(cfg.Apps))
	for _, app := range cfg.Apps {
		names = append(names, app.Name)
	}
	return fmt.Sprintf("config loads — %s (%s)", plural(len(names), "app"), strings.Join(names, ", "))
}

func providerText(descriptor *project.Provider) string {
	return "provider " + descriptor.ID + " " + version.Version
}

func edgeText(cfg *project.Project) string {
	if id := cfg.EdgeKind(); id != "" {
		return "edge " + string(id)
	}
	return "provider default edge"
}

type tierAnswer struct {
	status *contractv1.BootstrapStatus
}

type answers struct {
	providerName string
	problem      string
	identity     *contractv1.Identity
	problems     []*contractv1.CredentialProblem
	tiers        map[environmentv1.Tier]*tierAnswer
	hostChecks   []*contractv1.HostCheck
	hostnames    []*contractv1.ProductionHostname
	wildcard     *contractv1.PreviewWildcard
}

func (a *answers) addHostChecks(checks []*contractv1.HostCheck) {
	for _, check := range checks {
		seen := false
		for _, hostCheck := range a.hostChecks {
			if hostCheck.GetSubject() == check.GetSubject() && hostCheck.GetFinding() == check.GetFinding() {
				seen = true
				break
			}
		}
		if !seen {
			a.hostChecks = append(a.hostChecks, check)
		}
	}
}

func hostCheckDomains(asking bool, cfg *project.Project) []string {
	if !asking {
		return nil
	}
	var named []string
	for _, tier := range checkedTiers() {
		for _, hostname := range cfg.HostnameNames(tier) {
			if !slices.Contains(named, hostname) {
				named = append(named, hostname)
			}
		}
	}
	return named
}

func gather(ctx context.Context, invocation commands.Invocation, cfg *project.Project, providerID string) *answers {
	got := &answers{tiers: map[environmentv1.Tier]*tierAnswer{}}
	if err := checkSetup(ctx, invocation, cfg, providerID, got); err != nil && got.problem == "" {
		got.problem = strings.TrimSpace(err.Error())
	}
	return got
}

func checkSetup(ctx context.Context, invocation commands.Invocation, cfg *project.Project, providerID string, got *answers) error {
	ctx, run, err := invocation.Events.Begin(ctx, "ocel doctor", cfg.Dir)
	if err != nil {
		return err
	}
	defer func() {
		interrupted := ctx.Err()
		run.End(&interrupted)
	}()

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	var provider *providerprocess.Provider
	err = runStep(check, providerID, progress.Loading.Title("the provider"), func() (err error) {
		provider, _, err = invocation.OpenProvider(ctx, check, cfg, commands.OpenOptions{})
		return err
	})
	if err != nil {
		return err
	}
	defer provider.Close()
	got.providerName = provider.Name()

	for _, tier := range checkedTiers() {
		if err := askAboutTier(ctx, check, provider, cfg, tier, got); err != nil {
			return err
		}
	}
	return nil
}

func runStep(check *run.Span, subject string, title progress.Title, work func() error) error {
	span := check.Child(subject, title)
	err := work()
	span.End(err)
	return err
}

func askAboutTier(ctx context.Context, check *run.Span, provider *providerprocess.Provider, cfg *project.Project, tier environmentv1.Tier, got *answers) error {
	name := readiness.TierName(tier)
	checkHosts := tier == environmentv1.Tier_TIER_PRODUCTION
	var read readiness.Preflight
	err := runStep(check, provider.Name(), readiness.CheckingTitle(tier, cfg.Slug), func() (err error) {
		read, err = readiness.Read(ctx, provider, cfg, readiness.Request{
			Tier:             tier,
			Slug:             cfg.Slug,
			Domains:          cfg.HostnameNames(tier),
			CheckHosts:       checkHosts,
			HostCheckDomains: hostCheckDomains(checkHosts, cfg),
		})
		return err
	})
	if err != nil {
		return err
	}
	if got.identity == nil {
		got.identity = read.Response.GetIdentity()
	}
	got.keep(read.Response.GetCredentialProblems())
	got.addHostChecks(read.Response.GetHostChecks())
	if tier == environmentv1.Tier_TIER_PREVIEW {
		got.wildcard = read.Response.GetPreviewWildcard()
	}

	var planned *contractv1.DescribeBootstrapResponse
	err = runStep(check, provider.Name(), progress.Reading.Title("what "+name+" has set up"), func() error {
		return provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
			planned, err = client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{Tier: tier, Edge: cfg.EdgeSelection()})
			return err
		})
	})
	if err != nil {
		return err
	}
	got.tiers[tier] = &tierAnswer{status: planned.GetBootstrap()}
	if tier != environmentv1.Tier_TIER_PRODUCTION || !planned.GetBootstrap().GetPresent() {
		return nil
	}
	configured := cfg.ConfiguredHostnames(tier)
	if len(configured) == 0 {
		return nil
	}
	var bound *contractv1.GetHostnameStatusResponse
	err = runStep(check, provider.Name(), progress.Checking.Title("the "+name+" hostnames"), func() error {
		return provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
			bound, err = client.GetHostnameStatus(ctx, &contractv1.HostnameRequest{
				Slug:       cfg.Slug,
				Configured: configured,
				Edge:       cfg.EdgeSelection(),
			})
			return err
		})
	})
	if err != nil {
		return err
	}
	got.hostnames = bound.GetHostnames()
	return nil
}

func (a *answers) keep(problems []*contractv1.CredentialProblem) {
	for _, problem := range problems {
		seen := false
		for _, credentialProblem := range a.problems {
			if credentialProblem.GetProvider() == problem.GetProvider() && credentialProblem.GetMessage() == problem.GetMessage() {
				seen = true
				break
			}
		}
		if !seen {
			a.problems = append(a.problems, problem)
		}
	}
}

func credentialSections(cfg *project.Project, got *answers) []section {
	if got.problem != "" {
		s := section{name: "Provider", identity: got.providerName}
		if head, rest, multiline := strings.Cut(got.problem, "\n"); multiline {
			s.checks = append(s.checks, check{verdict: verdictFail, text: head, detail: strings.Split(rest, "\n")})
			return []section{s}
		}
		head, hint, _ := splitHint(got.problem)
		s.fail(head, hint)
		return []section{s}
	}

	claimed := make([]bool, len(got.problems))
	take := func(owner string) []check {
		var taken []check
		for i, problem := range got.problems {
			if claimed[i] || problem.GetProvider() != owner {
				continue
			}
			claimed[i] = true
			taken = append(taken, check{verdict: verdictFail, text: problem.GetMessage(), fix: problem.GetHint()})
		}
		return taken
	}

	identity := got.identity
	sections := []section{credentialsSection(
		titleOr(identity.GetProvider(), "Credentials"),
		identityText(identity),
		take(identity.GetProvider()),
	)}
	if scope := identity.GetEdgeScope(); scope != "" {
		edgeID := string(cfg.EdgeKind())
		sections = append(sections, credentialsSection(titleOr(edgeID, "Edge"), scope, take(edgeID)))
	}

	for i, problem := range got.problems {
		if claimed[i] {
			continue
		}
		claimed[i] = true
		name := titleOr(problem.GetProvider(), "Credentials")
		rejected := check{verdict: verdictFail, text: problem.GetMessage(), fix: problem.GetHint()}
		if at := sectionIndex(sections, name); at >= 0 {
			sections[at].checks = append(sections[at].checks, rejected)
			continue
		}
		sections = append(sections, section{name: name, checks: []check{rejected}})
	}
	return sections
}

func sectionIndex(sections []section, name string) int {
	for i, s := range sections {
		if s.name == name {
			return i
		}
	}
	return -1
}

func credentialsSection(name, identity string, checks []check) section {
	s := section{name: name, identity: identity, checks: checks}
	if len(checks) == 0 {
		s.pass("credentials valid")
	}
	return s
}

func identityText(identity *contractv1.Identity) string {
	var parts []string
	if account := identity.GetAccount(); account != "" {
		parts = append(parts, account)
	}
	if principal := identity.GetPrincipal(); principal != "" {
		parts = append(parts, principal)
	}
	if location := identity.GetLocation(); location != "" {
		parts = append(parts, location)
	}
	for _, detail := range identity.GetDetails() {
		value := detail.GetValue()
		if value == "" {
			continue
		}
		if label := detail.GetLabel(); label != "" {
			value = label + " " + value
		}
		parts = append(parts, value)
	}
	return join(parts...)
}

func tierSection(tier environmentv1.Tier, hosts []string, got *answers) section {
	name := readiness.TierName(tier)
	s := section{name: title(name), identity: strings.Join(hosts, ", ")}

	answer := got.tiers[tier]
	switch {
	case got.problem != "":
		s.neutral("skipped — the provider did not answer")
		return s
	case answer == nil:
		s.neutral("skipped — the provider did not answer")
		return s
	case answer.status == nil:
		s.fail("the provider answered with no "+name+" bootstrap status", "")
		return s
	}

	if tier == environmentv1.Tier_TIER_PREVIEW {
		previewDomain(&s, hosts, got.wildcard.GetBaseDomain())
	}

	status := answer.status
	if !status.GetPresent() {
		if len(hosts) == 0 {
			s.neutral(absentText(tier))
			return s
		}
		s.warn("not bootstrapped", "run `"+readiness.BootstrapCommand(tier)+"`")
		return s
	}

	if status.GetUnfinished() {
		s.fail("an apply never finished, so nothing recorded is a claim about what is provisioned",
			"run `"+readiness.BootstrapCommand(tier)+"` to plan the work that is left and finish it")
		return s
	}

	gap := readiness.NewGap(status)
	stale := staleStacks(status)
	if len(gap.Missing) == 0 && len(stale) == 0 {
		s.pass("bootstrapped, current")
		return s
	}
	if len(gap.Missing) > 0 {
		s.warn(listText(gap.Missing, "missing"), "run `"+gap.RepairCommand(tier)+"`")
	}
	if len(stale) > 0 {
		s.warn(listText(stale, "stale"), "run `"+readiness.BootstrapCommand(tier)+"` to refresh "+them(len(stale)))
	}
	return s
}

func staleStacks(status *contractv1.BootstrapStatus) []string {
	var out []string
	for _, stack := range status.GetStacks() {
		if stack.GetPresent() && !stack.GetDigestCurrent() {
			out = append(out, stack.GetName())
		}
	}
	return out
}

func previewDomain(s *section, hosts []string, global string) {
	switch {
	case len(hosts) == 0 && global == "":
		s.warn("no preview domain", "run `ocel domain use '*.preview.example.com' --preview`")
	case len(hosts) == 0:
		s.identity = "*." + global + " (global)"
	case global != "" && !slices.Contains(hosts, "*."+global):
		s.neutral("project-level preview domain; global *." + global + " ignored")
	}
}

func absentText(tier environmentv1.Tier) string {
	purpose := "set it up"
	if tier == environmentv1.Tier_TIER_PREVIEW {
		purpose = "add previews"
	}
	return "not set up — run `" + readiness.BootstrapCommand(tier) + "` to " + purpose
}

func listText(names []string, state string) string {
	verb := " is "
	if len(names) > 1 {
		verb = " are "
	}
	return strings.Join(names, ", ") + verb + state
}

func them(n int) string {
	if n > 1 {
		return "them"
	}
	return "it"
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func join(parts ...string) string {
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " · ")
}

func title(name string) string {
	return titleOr(name, "")
}

func titleOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	if len(name) <= 3 {
		return strings.ToUpper(name)
	}
	runes := []rune(name)
	return strings.ToUpper(string(runes[0])) + string(runes[1:])
}

func firstLine(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	return strings.TrimSpace(line)
}

func mark(p terminal.Palette, v verdict) string {
	switch v {
	case verdictFail:
		return p.FailMark()
	case verdictWarn:
		return p.WarnMark()
	case verdictNeutral:
		return p.NeutralMark()
	default:
		return p.PassMark()
	}
}

func (r report) render(out io.Writer, p terminal.Palette) {
	for i, s := range r.sections {
		if i > 0 {
			fmt.Fprintln(out)
		}
		s.render(out, p)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, r.summary(p))
}

func (s section) render(out io.Writer, p terminal.Palette) {
	fmt.Fprintln(out, heading(p, s.name, s.identity))
	for _, c := range s.checks {
		if c.verdict == verdictNeutral {
			fmt.Fprintf(out, "  %s\n", p.Faint("– "+c.text))
			continue
		}
		fmt.Fprintf(out, "  %s %s\n", mark(p, c.verdict), c.text)
		for _, line := range c.detail {
			fmt.Fprintf(out, "    %s\n", line)
		}
		if c.fix != "" {
			fmt.Fprintf(out, "    %s %s\n", p.Faint("→"), p.Commands(c.fix))
		}
	}
}

func (r report) result() *resultv1.DoctorResult {
	failures, warnings := r.failures(), r.warnings()
	overall := resultv1.DoctorVerdict_DOCTOR_VERDICT_PASS
	switch {
	case failures > 0:
		overall = resultv1.DoctorVerdict_DOCTOR_VERDICT_FAIL
	case warnings > 0:
		overall = resultv1.DoctorVerdict_DOCTOR_VERDICT_WARN
	}
	sections := make([]*resultv1.DoctorSection, 0, len(r.sections))
	for _, s := range r.sections {
		checks := make([]*resultv1.DoctorCheck, 0, len(s.checks))
		for _, c := range s.checks {
			checks = append(checks, &resultv1.DoctorCheck{
				Verdict: c.verdict.result(),
				Text:    c.text,
				Detail:  c.detail,
				Fix:     c.fix,
			})
		}
		sections = append(sections, &resultv1.DoctorSection{Name: s.name, Identity: s.identity, Checks: checks})
	}
	return &resultv1.DoctorResult{
		Verdict:  overall,
		Problems: int32(failures),
		Warnings: int32(warnings),
		Sections: sections,
	}
}

func (v verdict) result() resultv1.DoctorVerdict {
	switch v {
	case verdictPass:
		return resultv1.DoctorVerdict_DOCTOR_VERDICT_PASS
	case verdictWarn:
		return resultv1.DoctorVerdict_DOCTOR_VERDICT_WARN
	case verdictFail:
		return resultv1.DoctorVerdict_DOCTOR_VERDICT_FAIL
	case verdictNeutral:
		return resultv1.DoctorVerdict_DOCTOR_VERDICT_NEUTRAL
	default:
		return resultv1.DoctorVerdict_DOCTOR_VERDICT_UNSPECIFIED
	}
}

func (r report) summary(p terminal.Palette) string {
	failures, warnings := r.failures(), r.warnings()
	if failures == 0 && warnings == 0 {
		return p.SuccessBold("Good to go.")
	}
	var parts []string
	if failures > 0 {
		parts = append(parts, p.FailureBold(plural(failures, "problem")))
	}
	if warnings > 0 {
		parts = append(parts, p.Warning(plural(warnings, "warning")))
	}
	return strings.Join(parts, ", ") + "."
}

func heading(p terminal.Palette, name, identity string) string {
	if identity == "" {
		return p.Bold(name)
	}
	return p.Bold(name) + "  " + p.Faint(identity)
}
