import { spawnSync } from "node:child_process";

function gt(args: string[], cwd: string) {
	return spawnSync("gt", [...args, "--no-interactive"], { cwd, encoding: "utf8" });
}

export function gtTrack(branch: string, parent: string, cwd: string) {
	const res = gt(["track", branch, "--parent", parent], cwd);
	if (res.status !== 0) {
		throw new Error(`gt track ${branch} --parent ${parent} failed: ${res.stderr.trim()}`);
	}
}

export function gtSync(cwd: string) {
	const res = gt(["sync", "--force"], cwd);
	if (res.status !== 0) {
		throw new Error(`gt sync failed: ${res.stderr.trim()}`);
	}
}
