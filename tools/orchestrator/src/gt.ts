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
// parent. Returns `branch`'s own PR URL, or null if not found in the output.
//
// A single `gt submit --branch X` call also submits X's untracked ancestors,
// printing one "<branch>: <url> (created|updated|no-op)" line per branch it
// touched — so stdout can list several branches' URLs, not just `branch`'s.
export function gtSubmit(branch: string, cwd: string): string | null {
	const res = gt(["submit", "--branch", branch, "--no-edit", "--publish"], cwd);
	if (res.status !== 0) {
		throw new Error(`gt submit --branch ${branch} failed: ${res.stderr.trim()}`);
	}
	// eslint-disable-next-line no-control-regex -- stripping ANSI color codes
	const plain = res.stdout.replace(/\x1b\[[0-9;]*m/g, "");
	const line = plain.split("\n").find((l) => l.trim().startsWith(`${branch}:`));
	const match = line?.match(/(https:\/\/\S+)/);
	return match?.[1] ?? null;
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
