import { spawnSync } from "node:child_process";

export const FAILED_LABEL = "orchestrator-failed";

export interface BdIssue {
	id: string;
	title: string;
	[key: string]: unknown;
}

function bd(args: string[], cwd: string) {
	return spawnSync("bd", args, { cwd, encoding: "utf8" });
}

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

export function claimBatch(parentId: string, repoRoot: string, maxParallelIssues: number): BdIssue[] {
	const batch: BdIssue[] = [];
	for (let i = 0; i < maxParallelIssues; i++) {
		const issue = claimNextReadyIssue(parentId, repoRoot);
		if (!issue) break;
		batch.push(issue);
	}
	return batch;
}

export function revertClaim(issueId: string, repoRoot: string, log: (msg: string) => void) {
	const res = bd(["update", issueId, "--status=open", "--assignee=", "--add-label", FAILED_LABEL], repoRoot);
	if (res.status !== 0) {
		log(`Failed to revert claim on ${issueId}: ${res.stderr.trim()}`);
	}
}

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
