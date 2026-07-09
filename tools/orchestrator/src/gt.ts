// Thin wrapper around the Graphite (`gt`) CLI for the host-side steps the
// orchestrator runs itself — tracking the base branch and syncing the stack at
// the top of each wave. Agents never see `gt` or a GitHub token. The per-wave
// track/submit of finished branches is delegated to a model call (see
// submit.ts), which shells out to `gt` directly. `gt track` operates on a named
// branch without needing it checked out, so no `git checkout` is required.

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
