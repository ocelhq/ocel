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
	return `ocel/issue-${issue.id}-${slugify(issue.title)}`;
}

// Pushes `branch` and opens a PR against `baseBranch` from the host — the
// sandbox never holds a GitHub token. Returns the PR URL, or null if `gh`
// reports the branch has no diff against base (nothing to open a PR for).
export function pushAndOpenPr(branch: string, baseBranch: string, title: string, body: string, repoRoot: string): string | null {
	git(["push", "-u", "origin", branch], repoRoot);
	const res = spawnSync("gh", ["pr", "create", "--base", baseBranch, "--head", branch, "--title", title, "--body", body], {
		cwd: repoRoot,
		encoding: "utf8",
	});
	if (res.status !== 0) {
		throw new Error(`gh pr create failed for ${branch}: ${res.stderr.trim()}`);
	}
	return res.stdout.trim();
}
