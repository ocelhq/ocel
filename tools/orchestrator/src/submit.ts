// Delegates a wave's final Graphite work — tracking each closed issue's branch
// onto its parent and submitting the stack — to a single host-side `claude` CLI
// call. Graphite's fixed track→submit sequence is fragile (a stacked branch may
// need a restack/rebase before `gt submit` will take), so instead of hand-coding
// each error path we hand the model the same steps and let it work around
// whatever it hits. Runs on the host, which already has `gt`/`gh`/`bd`
// authenticated — sandboxes never see a GitHub token.

import { spawnSync } from "node:child_process";
import { appendFileSync, readFileSync } from "node:fs";
import path from "node:path";
import { type BdIssue, revertClaim } from "./bd.ts";

// Match the model used for implement runs (orchestrator.ts).
const SUBMIT_MODEL = "claude-sonnet-5";
const SUBMIT_TIMEOUT_MS = 20 * 60 * 1000;

export interface WaveOutcome {
	issue: BdIssue;
	branch: string;
	parentBranch: string;
}

function renderBranchList(outcomes: WaveOutcome[]): string {
	return outcomes
		.map(({ issue, branch, parentBranch }) => `- ISSUE_ID: ${issue.id}\n  BRANCH: ${branch}\n  PARENT_BRANCH: ${parentBranch}`)
		.join("\n");
}

// Tracks and submits every branch produced by this wave via a host-side model
// call. The model reopens any branch it can't submit (bd update --add-label
// orchestrator-failed) itself; the host only steps in if the `claude` process
// never ran to completion, reverting the whole wave so no closed issue is left
// with no PR.
export async function submitWaveWithModel(
	outcomes: WaveOutcome[],
	repoRoot: string,
	runDir: string,
	waveIndex: number,
	log: (msg: string) => void,
): Promise<void> {
	if (outcomes.length === 0) return;

	const branches = outcomes.map((o) => o.branch).join(", ");
	log(`Delegating track/submit for ${outcomes.length} branch(es) to a model call: ${branches}`);

	const template = readFileSync(path.join(repoRoot, ".sandcastle", "submit-prompt.md"), "utf8");
	const prompt = template.replace("{{BRANCHES}}", renderBranchList(outcomes));

	const res = spawnSync(
		"claude",
		["--print", "--dangerously-skip-permissions", "--model", SUBMIT_MODEL, "-p", "-"],
		{ cwd: repoRoot, input: prompt, encoding: "utf8", timeout: SUBMIT_TIMEOUT_MS, maxBuffer: 64 * 1024 * 1024 },
	);

	const logFile = path.join(runDir, `submit-wave-${waveIndex}.log`);
	appendFileSync(logFile, `${res.stdout ?? ""}\n--- stderr ---\n${res.stderr ?? ""}\n`);

	if (res.error || res.status !== 0) {
		const reason = res.error ? res.error.message : `exited with status ${res.status}`;
		log(`Submit model call failed (${reason}). Reverting claim on this wave's issues for human follow-up.`);
		for (const { issue } of outcomes) revertClaim(issue.id, repoRoot, log);
		return;
	}

	const summary = (res.stdout ?? "")
		.trim()
		.split("\n")
		.reverse()
		.find((l) => l.trim().startsWith("{"));
	log(`Submit model call finished. Summary: ${summary ?? "(no JSON summary in output; see " + path.basename(logFile) + ")"}`);
}
