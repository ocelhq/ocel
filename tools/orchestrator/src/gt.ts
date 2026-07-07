// Thin wrapper around the Graphite (`gt`) CLI. All calls run on the host —
// agents never see `gt` or a GitHub token. `gt track`/`gt submit --branch`
// both operate on a named branch without needing it checked out, so the
// orchestrator never has to `git checkout` between issues.

import { spawnSync } from "node:child_process";

function gt(args: string[], cwd: string) {
	return spawnSync("gt", [...args, "--no-interactive"], { cwd, encoding: "utf8" });
}

// Registers `branch` with Graphite as a child of `parent`. `parent` must
// already be tracked. Idempotent — re-tracking an already-tracked branch
// just updates its parent.
export function gtTrack(branch: string, parent: string, cwd: string) {
	const res = gt(["track", branch, "--parent", parent], cwd);
	if (res.status !== 0) {
		throw new Error(`gt track ${branch} --parent ${parent} failed: ${res.stderr.trim()}`);
	}
}

// Pushes `branch` (and any untracked ancestors up to trunk) and creates or
// updates its PR, with the PR base following whatever `gtTrack` set as its
// parent. Returns the PR URL Graphite prints, or null if none was printed.
export function gtSubmit(branch: string, cwd: string): string | null {
	const res = gt(["submit", "--branch", branch, "--no-edit", "--publish"], cwd);
	if (res.status !== 0) {
		throw new Error(`gt submit --branch ${branch} failed: ${res.stderr.trim()}`);
	}
	const match = res.stdout.match(/https:\/\/(?:app\.graphite\.com|github\.com)\S+/);
	return match?.[0] ?? null;
}

// Pulls trunk, deletes merged/closed branches, and restacks descendants.
// Throws (with gt's own message, which names the conflicting branch) if a
// restack hits a conflict — callers should surface this to a human rather
// than guess at a resolution.
export function gtSync(cwd: string) {
	const res = gt(["sync", "--force"], cwd);
	if (res.status !== 0) {
		throw new Error(`gt sync failed: ${res.stderr.trim()}`);
	}
}
