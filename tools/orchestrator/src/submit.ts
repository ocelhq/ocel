import { spawnSync } from "node:child_process";
import { appendFileSync, readFileSync } from "node:fs";
import path from "node:path";
import { type BdIssue, revertClaim } from "./bd.ts";

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
