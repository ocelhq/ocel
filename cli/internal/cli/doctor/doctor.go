package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func NewCommand(deps cmddeps.Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that everything is good to go",
		Long: "Check that everything is good to go.\n\n" +
			"Reads the project, the cloud credentials it reaches with, and what production and " +
			"preview have set up, then names the fix for anything standing in the way. " +
			"Nothing is created or changed.",
		Example: "  $ ocel doctor",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			ctx, stop := deps.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()

			return Run(ctx, deps, cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func Run(ctx context.Context, deps cmddeps.Deps, cwd string, stdout, stderr io.Writer) error {
	found := build(ctx, deps, cwd, stdout, stderr)
	found.render(stdout, newPaint(stdout))
	if found.failures() > 0 {
		return &exitsig.ExitError{Code: 1}
	}
	return nil
}

const (
	passGlyph    = "✓"
	failGlyph    = "✗"
	warnGlyph    = "⚠"
	neutralGlyph = "–"
)

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

var tiers = []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW}

func build(ctx context.Context, deps cmddeps.Deps, cwd string, stdout, stderr io.Writer) report {
	var found report
	project := section{name: "Project"}
	project.checks = append(project.checks, nodeCheck(ctx))

	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		project.fail(configFailure(err))
		found.add(project)
		return found
	}

	project.identity = join(cfg.Slug, filepath.Base(cfg.Path))
	project.pass(appsText(cfg))

	descriptor, providerErr := cfg.RequireProvider()
	if providerErr != nil {
		project.fail(firstLine(providerErr.Error()), "")
	} else {
		project.pass(providerText(cfg, descriptor))
	}
	project.pass(edgeText(cfg))

	hosts := map[environmentv1.Tier][]string{}
	for _, tier := range tiers {
		hosts[tier] = preflight.Hostnames(cfg, bootstrap.Name(tier))
	}
	if len(hosts[environmentv1.Tier_TIER_PREVIEW]) == 0 {
		project.warn("no preview domain declared", "add `domains: { preview: \"*.preview.example.com\" }` to your config")
	}
	found.add(project)

	if providerErr != nil {
		found.add(skippedSection("Credentials"))
		for _, tier := range tiers {
			found.add(skippedSection(title(bootstrap.Name(tier))))
		}
		return found
	}

	answers := gather(ctx, deps, cfg, stdout, stderr)
	found.add(credentialSections(cfg, answers)...)
	for _, tier := range tiers {
		found.add(tierSection(tier, hosts[tier], answers))
	}
	return found
}

func skippedSection(name string) section {
	s := section{name: name}
	s.neutral("skipped — no provider to check with")
	return s
}

func nodeCheck(ctx context.Context) check {
	path, err := exec.LookPath("node")
	if err != nil {
		return check{verdict: verdictFail, text: "node not found on PATH", fix: "install Node.js and put it on PATH"}
	}
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	version := strings.TrimSpace(string(out))
	if err != nil || version == "" {
		return check{verdict: verdictPass, text: "node on PATH"}
	}
	return check{verdict: verdictPass, text: "node " + version + " on PATH"}
}

func configFailure(err error) (string, string) {
	message := firstLine(err.Error())
	if strings.Contains(message, "no "+projectconfig.ConfigFileName+" found") {
		return "no " + projectconfig.ConfigFileName + " found in this directory or any parent",
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

func appsText(cfg *projectconfig.Config) string {
	if len(cfg.Apps) == 0 {
		return "config loads — no apps declared"
	}
	names := make([]string, 0, len(cfg.Apps))
	for _, app := range cfg.Apps {
		names = append(names, app.Name)
	}
	return fmt.Sprintf("config loads — %s (%s)", plural(len(names), "app"), strings.Join(names, ", "))
}

func providerText(cfg *projectconfig.Config, descriptor *projectconfig.ProviderDescriptor) string {
	text := "provider " + descriptor.Package
	if version := packageVersion(cfg.Dir, descriptor.Package); version != "" {
		text += " " + version
	}
	return text
}

func packageVersion(dir, pkg string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "node_modules", filepath.FromSlash(pkg), "package.json"))
	if err != nil {
		return ""
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ""
	}
	return manifest.Version
}

func edgeText(cfg *projectconfig.Config) string {
	if kind := cfg.EdgeKind(); kind != "" {
		return "edge " + string(kind)
	}
	return "provider default edge"
}

type tierAnswer struct {
	status  *contractv1.BootstrapStatus
	problem string
}

type answers struct {
	pkg      string
	problem  string
	fix      string
	identity *contractv1.Identity
	problems []*contractv1.CredentialProblem
	tiers    map[environmentv1.Tier]*tierAnswer
}

const upgradeProvider = "upgrade the provider pinned in this project"

func gather(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, stdout, stderr io.Writer) *answers {
	got := &answers{tiers: map[environmentv1.Tier]*tierAnswer{}}

	spinner := deployui.StartSpinner(stdout, "Checking your setup")
	err := provider.Drive(ctx, cfg, stderr, stderr, deps.HostTrust, func(runner *provider.Runner) error {
		*got = answers{tiers: map[environmentv1.Tier]*tierAnswer{}}
		got.pkg = runner.Package()
		client, err := runner.Client()
		if err != nil {
			return err
		}
		for _, tier := range tiers {
			resp, err := client.Preflight(ctx, &contractv1.PreflightRequest{
				RequiredTier: tier,
				Slug:         cfg.Slug,
				Domains:      preflight.Hostnames(cfg, bootstrap.Name(tier)),
				Frameworks:   preflight.Frameworks(cfg),
				Edge:         edgewire.Selection(cfg),
			})
			if err != nil {
				if connect.CodeOf(err) == connect.CodeUnimplemented {
					got.problem = got.pkg + " cannot check credentials; it predates the check"
					got.fix = upgradeProvider
					return nil
				}
				return err
			}
			if got.identity == nil {
				got.identity = resp.GetIdentity()
			}
			got.keep(resp.GetCredentialProblems())

			planned, err := client.PlanBootstrap(ctx, &contractv1.PlanBootstrapRequest{Tier: tier, Edge: edgewire.Selection(cfg)})
			if err != nil {
				if connect.CodeOf(err) == connect.CodeUnimplemented {
					got.tiers[tier] = &tierAnswer{problem: got.pkg + " cannot report what a bootstrap has; it predates the report"}
					continue
				}
				return err
			}
			got.tiers[tier] = &tierAnswer{status: planned.GetBootstrap()}
		}
		return nil
	})
	spinner.Stop()

	if err != nil && got.problem == "" {
		got.problem = strings.TrimSpace(err.Error())
	}
	return got
}

func (a *answers) keep(problems []*contractv1.CredentialProblem) {
	for _, problem := range problems {
		seen := false
		for _, held := range a.problems {
			if held.GetProvider() == problem.GetProvider() && held.GetMessage() == problem.GetMessage() {
				seen = true
				break
			}
		}
		if !seen {
			a.problems = append(a.problems, problem)
		}
	}
}

func credentialSections(cfg *projectconfig.Config, got *answers) []section {
	if got.problem != "" {
		s := section{name: "Provider", identity: got.pkg}
		if head, rest, multiline := strings.Cut(got.problem, "\n"); multiline {
			s.checks = append(s.checks, check{verdict: verdictFail, text: head, detail: strings.Split(rest, "\n")})
			return []section{s}
		}
		head, hint, ok := splitHint(got.problem)
		if !ok {
			head, hint = got.problem, got.fix
		}
		s.fail(head, hint)
		return []section{s}
	}

	claimed := make([]bool, len(got.problems))
	take := func(owner string) []check {
		var taken []check
		for i, problem := range got.problems {
			if claimed[i] || !strings.EqualFold(problem.GetProvider(), owner) {
				continue
			}
			claimed[i] = true
			taken = append(taken, check{verdict: verdictFail, text: problem.GetMessage(), fix: problem.GetHint()})
		}
		return taken
	}

	identity := got.identity
	sections := []section{holder(
		titleOr(identity.GetProvider(), "Credentials"),
		identityText(identity),
		take(identity.GetProvider()),
	)}
	if scope := identity.GetEdgeScope(); scope != "" {
		kind := string(cfg.EdgeKind())
		sections = append(sections, holder(titleOr(kind, "Edge"), scope, take(kind)))
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

func holder(name, identity string, checks []check) section {
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
	name := bootstrap.Name(tier)
	s := section{name: title(name), identity: strings.Join(hosts, ", ")}

	answer := got.tiers[tier]
	switch {
	case got.problem != "":
		s.neutral("skipped — the provider did not answer")
		return s
	case answer == nil:
		s.neutral("skipped — the provider did not answer")
		return s
	case answer.problem != "":
		s.fail(answer.problem, upgradeProvider)
		return s
	case answer.status == nil:
		s.fail("the provider said nothing about the "+name+" bootstrap", upgradeProvider)
		return s
	}

	status := answer.status
	if !status.GetPresent() {
		if len(hosts) == 0 {
			s.neutral(absentText(tier))
			return s
		}
		s.warn("not bootstrapped", "run `ocel bootstrap "+name+"`")
		return s
	}

	if status.GetUnfinished() {
		s.fail("an apply never finished, so nothing recorded is a claim about what stands",
			"run `ocel bootstrap "+name+"` to plan the work that is left and finish it")
		return s
	}

	schemas := fmt.Sprintf("bootstrap schema %d, this CLI speaks schema %d", status.GetSchema(), status.GetRequiredSchema())
	switch {
	case status.GetSchema() > status.GetRequiredSchema():
		s.fail(schemas, "upgrade the Ocel CLI")
		return s
	case status.GetSchema() < status.GetRequiredSchema():
		s.warn(schemas, "run `ocel bootstrap "+name+"` to upgrade it")
		return s
	}

	plan := bootstrap.PlanFor(status)
	stale := staleStacks(status)
	if len(plan.Missing) == 0 && len(stale) == 0 {
		s.pass(fmt.Sprintf("bootstrapped — schema %d, current", status.GetSchema()))
		return s
	}
	if len(plan.Missing) > 0 {
		s.warn(listText(plan.Missing, "missing"), "run `"+plan.Command(tier)+"`")
	}
	if len(stale) > 0 {
		s.warn(listText(stale, "stale"), "run `ocel bootstrap "+name+"` to refresh "+them(len(stale)))
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

func absentText(tier environmentv1.Tier) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return "not set up — run `ocel bootstrap preview` to add previews"
	}
	return "not set up — run `ocel bootstrap production` to set it up"
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

type paint struct {
	bold    *color.Color
	dim     *color.Color
	good    *color.Color
	pass    *color.Color
	warn    *color.Color
	fail    *color.Color
	command *color.Color
}

func newPaint(out io.Writer) paint {
	return paint{
		bold:    tint(out, color.Bold),
		dim:     tint(out, color.Faint),
		good:    tint(out, color.FgGreen, color.Bold),
		pass:    tint(out, color.FgGreen),
		warn:    tint(out, color.FgYellow),
		fail:    tint(out, color.FgRed, color.Bold),
		command: tint(out, color.FgCyan),
	}
}

func tint(out io.Writer, attrs ...color.Attribute) *color.Color {
	c := color.New(attrs...)
	if deployui.IsTerminal(out) && !color.NoColor {
		c.EnableColor()
	} else {
		c.DisableColor()
	}
	return c
}

func (p paint) glyph(v verdict) string {
	switch v {
	case verdictFail:
		return p.fail.Sprint(failGlyph)
	case verdictWarn:
		return p.warn.Sprint(warnGlyph)
	case verdictNeutral:
		return p.dim.Sprint(neutralGlyph)
	default:
		return p.pass.Sprint(passGlyph)
	}
}

func (p paint) hint(text string) string {
	parts := strings.Split(text, "`")
	if len(parts)%2 == 0 {
		return text
	}
	for i := 1; i < len(parts); i += 2 {
		parts[i] = p.command.Sprint("`" + parts[i] + "`")
	}
	return strings.Join(parts, "")
}

func (r report) render(out io.Writer, p paint) {
	for i, s := range r.sections {
		if i > 0 {
			fmt.Fprintln(out)
		}
		s.render(out, p)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, r.summary(p))
}

func (s section) render(out io.Writer, p paint) {
	fmt.Fprintln(out, heading(p, s.name, s.identity))
	for _, c := range s.checks {
		if c.verdict == verdictNeutral {
			fmt.Fprintf(out, "  %s\n", p.dim.Sprint(neutralGlyph+" "+c.text))
			continue
		}
		fmt.Fprintf(out, "  %s %s\n", p.glyph(c.verdict), c.text)
		for _, line := range c.detail {
			fmt.Fprintf(out, "    %s\n", line)
		}
		if c.fix != "" {
			fmt.Fprintf(out, "    %s %s\n", p.dim.Sprint("→"), p.hint(c.fix))
		}
	}
}

func (r report) summary(p paint) string {
	failures, warnings := r.count(verdictFail), r.count(verdictWarn)
	if failures == 0 && warnings == 0 {
		return p.good.Sprint("Good to go.")
	}
	var parts []string
	if failures > 0 {
		parts = append(parts, p.fail.Sprint(plural(failures, "problem")))
	}
	if warnings > 0 {
		parts = append(parts, p.warn.Sprint(plural(warnings, "warning")))
	}
	return strings.Join(parts, ", ") + "."
}

func heading(p paint, name, identity string) string {
	if identity == "" {
		return p.bold.Sprint(name)
	}
	return p.bold.Sprint(name) + "  " + p.dim.Sprint(identity)
}
