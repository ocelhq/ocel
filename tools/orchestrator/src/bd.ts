// Thin wrapper around the `bd` (beads) CLI. All calls run against the host's
// working tree — the sandbox mounts the same `.beads/` directory (see
// orchestrator.ts), so writes an agent makes inside the container are visible
// here immediately.

import { spawnSync } from "node:child_process";

// Applied to an issue when its implement attempt fails, so future claims in
// this run skip it instead of retrying it forever.
export const FAILED_LABEL = "orchestrator-failed";

export interface BdIssue {
	id: string;
	title: string;
	[key: string]: unknown;
}

function bd(args: string[], cwd: string) {
	return spawnSync("bd", args, { cwd, encoding: "utf8" });
}

// Resolves the real `.beads` directory bd is using from `cwd` — bd finds
// this via git's own common-dir resolution, so it correctly points at the
// main checkout's `.beads` even when `cwd` is a linked worktree (a worktree
// has its own bare-bones `.beads/` client stub with no issue data of its
// own, so mounting that into a sandbox instead of this path leaves agents
// unable to see any issues).
export function bdWhere(cwd: string): string {
	const res = bd(["where"], cwd);
	if (res.status !== 0) {
		throw new Error(`bd where failed: ${res.stderr.trim()}`);
	}
	const beadsDir = res.stdout.trim().split("\n")[0];
	if (!beadsDir) {
		throw new Error(`bd where produced no output from ${cwd}`);
	}
	return beadsDir;
}

// Atomically claims the next unblocked, ready-for-agent bd issue under
// `parentId` (sets its assignee and status: in_progress), or returns null if
// none remain.
function claimNextReadyIssue(parentId: string, repoRoot: string): BdIssue | null {
	const res = bd(
		["ready", "--claim", "--json", "--parent", parentId, "--label", "ready-for-agent", "--exclude-label", FAILED_LABEL],
		repoRoot,
	);
	if (res.status !== 0) {
		throw new Error(`bd ready --claim failed: ${res.stderr.trim()}`);
	}
	let issues: BdIssue[];
	try {
		issues = JSON.parse(res.stdout);
	} catch {
		throw new Error(`bd ready --claim produced non-JSON output: ${res.stdout.slice(0, 2000)}`);
	}
	return issues[0] ?? null;
}

export function claimBatch(parentId: string, repoRoot: string, maxCount: number): BdIssue[] {
	const batch: BdIssue[] = [];
	for (let i = 0; i < maxCount; i++) {
		const issue = claimNextReadyIssue(parentId, repoRoot);
		if (!issue) break;
		batch.push(issue);
	}
	return batch;
}

// Reverts a failed attempt: reopens the issue, clears its assignee, and
// labels it so this run's future claims skip it instead of retrying forever.
export function revertClaim(issueId: string, repoRoot: string, log: (msg: string) => void) {
	const res = bd(["update", issueId, "--status=open", "--assignee=", "--add-label", FAILED_LABEL], repoRoot);
	if (res.status !== 0) {
		log(`Failed to revert claim on ${issueId}: ${res.stderr.trim()}`);
	}
}

// Reads the issue's current status straight from the shared beads database.
export function issueStatus(issueId: string, repoRoot: string): string | null {
	const res = bd(["show", issueId, "--json"], repoRoot);
	if (res.status !== 0) return null;
	try {
		const data = JSON.parse(res.stdout);
		const issue = Array.isArray(data) ? data[0] : data;
		return issue?.status ?? null;
	} catch {
		return null;
	}
}

export interface BdBlocker {
	id: string;
	title: string;
}

// Returns the issues that block `issueId` (its "blocks"-type dependencies —
// bd's `dependencies` array also includes the parent-epic link, which isn't
// a blocker and is excluded here). Since `bd ready --claim` only surfaces
// issues whose blockers are all closed, every entry returned here is already
// closed by the time an orchestrator run sees it.
export function issueBlockers(issueId: string, repoRoot: string): BdBlocker[] {
	const res = bd(["show", issueId, "--json"], repoRoot);
	if (res.status !== 0) return [];
	try {
		const data = JSON.parse(res.stdout);
		const issue = Array.isArray(data) ? data[0] : data;
		const deps = (issue?.dependencies ?? []) as Array<{ id: string; title: string; dependency_type: string }>;
		return deps.filter((d) => d.dependency_type === "blocks").map((d) => ({ id: d.id, title: d.title }));
	} catch {
		return [];
	}
}
