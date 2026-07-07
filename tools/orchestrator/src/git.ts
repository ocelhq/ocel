import { spawnSync } from "node:child_process";

export function git(args: string[], cwd: string): string {
	const res = spawnSync("git", args, { cwd, encoding: "utf8" });
	if (res.status !== 0) {
		throw new Error(`git ${args.join(" ")} failed: ${res.stderr.trim()}`);
	}
	return res.stdout.trim();
}

export function remoteBranchExists(branch: string, cwd: string): boolean {
	const res = spawnSync("git", ["ls-remote", "--exit-code", "--heads", "origin", branch], { cwd, encoding: "utf8" });
	return res.status === 0;
}

export function slugify(text: string): string {
	return text
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "")
		.slice(0, 50);
}

export function branchNameFor(issue: { id: string; title: string }): string {
	return `claude/issue-${issue.id}-${slugify(issue.title)}`;
}

export function localBranchExists(branch: string, cwd: string): boolean {
	const res = spawnSync("git", ["rev-parse", "--verify", "--quiet", branch], { cwd, encoding: "utf8" });
	return res.status === 0;
}

// True if `branch` is already fully merged into `base` (i.e. base contains
// every commit on branch) — used to tell an already-landed blocker apart
// from one whose branch still needs to be the stack parent.
export function isMergedInto(branch: string, base: string, cwd: string): boolean {
	const res = spawnSync("git", ["merge-base", "--is-ancestor", branch, base], { cwd, encoding: "utf8" });
	return res.status === 0;
}
