// Claims ready-for-agent bd issues under a given parent (epic/PRD) and
// implements them one batch at a time, each in its own Docker sandbox (via
// sandcastle) on its own branch. Agents never see a GitHub token — once a
// sandboxed run closes its bd issue, the host tracks and submits the branch
// with Graphite: an issue whose bd blockers are still-unmerged branches
// stacks on the last unmerged blocker's branch; otherwise it's a sibling off
// the run's feature branch. See
// `.scratch/<parent-id>/orchestrator-runs/<timestamp>/` for per-run logs.

import { spawnSync } from "node:child_process";
import { appendFileSync, mkdirSync } from "node:fs";
import path from "node:path";
import { claudeCode, run as sandcastleRun } from "@ai-hero/sandcastle";
import { docker } from "@ai-hero/sandcastle/sandboxes/docker";
import { bdWhere, claimBatch, issueBlockers, issueStatus, revertClaim } from "./bd.ts";
import { branchNameFor, git, isMergedInto, localBranchExists, remoteBranchExists } from "./git.ts";
import { gtSubmit, gtSync, gtTrack } from "./gt.ts";
import type { RunInfra } from "./infra.ts";
import { setupRunInfra } from "./infra.ts";

const TRUNK_BRANCH = "main";

// Max issues run concurrently (in separate sandboxes) within one supercycle.
const MAX_ITERATIONS = 3;
// Safety cap on the number of re-claim batches, in case something keeps
// re-claiming without making progress.
const MAX_SUPERCYCLES = 20;
const IDLE_TIMEOUT_SECONDS = 30 * 60;
const IMAGE_NAME = "sandcastle:ocelhq";

function usageError(message: string): never {
	console.error(message);
	console.error("Usage: pnpm --filter @ocel/orchestrator orchestrate <parent-issue-id>");
	process.exit(1);
}

// Picks the branch a new issue branch should stack on: the last of its bd
// blockers whose branch still exists and isn't merged into `baseBranch` yet,
// or `baseBranch` itself if every blocker has already landed (or it has
// none). `bd ready --claim` only surfaces issues whose blockers are all bd-
// closed, and — because claiming is sequential while implement runs are the
// only thing that happens concurrently — a blocker can only be bd-closed by
// an earlier supercycle, so its branch is guaranteed to already be tracked.
function resolveParentBranch(issue: { id: string }, baseBranch: string, repoRoot: string): string {
	const blockers = issueBlockers(issue.id, repoRoot);
	const candidates = blockers
		.map((blocker) => branchNameFor(blocker))
		.filter((branch) => localBranchExists(branch, repoRoot) && !isMergedInto(branch, baseBranch, repoRoot));

	if (candidates.length <= 1) return candidates[0] ?? baseBranch;

	// More than one still-unmerged blocker: if they form a single Graphite
	// stack (each is already merged into one further-along candidate's
	// branch), stack on the deepest one — it already contains every other
	// candidate's commits. If they don't form a chain (independent
	// branches), there's no single correct parent; keep the old last-wins
	// behavior as a fallback.
	const deepest = candidates.find((candidate) =>
		candidates.every((other) => other === candidate || isMergedInto(other, candidate, repoRoot)),
	);
	return deepest ?? candidates[candidates.length - 1] ?? baseBranch;
}

function ensureImageBuilt(repoRoot: string, sandcastleBin: string, log: (msg: string) => void) {
	const inspect = spawnSync("docker", ["image", "inspect", IMAGE_NAME], { encoding: "utf8" });
	if (inspect.status === 0) return;
	log(`Image ${IMAGE_NAME} not found — building from .sandcastle/Dockerfile...`);
	const build = spawnSync(sandcastleBin, ["docker", "build-image", "--image-name", IMAGE_NAME], {
		cwd: repoRoot,
		encoding: "utf8",
		stdio: "inherit",
	});
	if (build.status !== 0) {
		throw new Error(`sandcastle docker build-image failed with status ${build.status}`);
	}
}

async function main() {
	// Node skips `finally` blocks on SIGINT/SIGTERM (Ctrl+C or a container
	// stop), which would otherwise leak the run's Docker network and Postgres
	// sidecar. `infra` is set once setupRunInfra() below returns.
	let infra: RunInfra | undefined;
	for (const signal of ["SIGINT", "SIGTERM"] as const) {
		process.on(signal, () => {
			console.error(`Received ${signal}, tearing down run infra...`);
			infra?.teardown();
			process.exit(1);
		});
	}

	const parentId = process.argv[2];
	if (!parentId) usageError("Missing <parent-issue-id> argument.");

	const repoRoot = git(["rev-parse", "--show-toplevel"], process.cwd());
	const sandcastleBin = path.join(repoRoot, "tools", "orchestrator", "node_modules", ".bin", "sandcastle");

	const parentCheck = spawnSync("bd", ["show", parentId, "--json"], { cwd: repoRoot, encoding: "utf8" });
	if (parentCheck.status !== 0) {
		usageError(`No bd issue found with id "${parentId}". Run 'bd list --type=epic' to find the right id.`);
	}

	const gitStatus = git(["status", "--porcelain"], repoRoot);
	if (gitStatus) {
		console.error("Working tree is not clean. Commit or stash changes before running the orchestrator.");
		process.exit(1);
	}

	const baseBranch = git(["rev-parse", "--abbrev-ref", "HEAD"], repoRoot);
	if (!remoteBranchExists(baseBranch, repoRoot)) {
		console.log(`Pushing base branch ${baseBranch} to origin...`);
		git(["push", "-u", "origin", baseBranch], repoRoot);
	}

	const runTimestamp = new Date().toISOString().replace(/[:.]/g, "-");
	const runId = runTimestamp.toLowerCase();
	const runDir = path.join(repoRoot, ".scratch", parentId, "orchestrator-runs", runTimestamp);
	mkdirSync(runDir, { recursive: true });

	function log(msg: string) {
		const line = `[${new Date().toISOString()}] ${msg}`;
		console.log(line);
		appendFileSync(path.join(runDir, "run.log"), `${line}\n`);
	}

	log(`Starting orchestrator run for parent "${parentId}" on base branch "${baseBranch}"`);

	ensureImageBuilt(repoRoot, sandcastleBin, log);

	infra = setupRunInfra(runId);
	log(`Run infra ready: network ${infra.networkName}, Postgres sidecar ${infra.postgresContainerName}`);

	// bd resolves this via git's own common-dir logic, so it's correct whether
	// the orchestrator runs from the main checkout or a linked worktree — a
	// worktree's own `.beads/` is just a client stub with no issue data.
	const beadsDir = bdWhere(repoRoot);
	const beadsMount = { hostPath: beadsDir, sandboxPath: beadsDir };

	// Hazard confirmed by testing: once a branch is gt-tracked, a later `gt
	// sync` restack can silently drop commits made on it via plain `git
	// commit` since gt's last look at it (e.g. hand-editing this repo on
	// `baseBranch` between runs, or in the same checkout the orchestrator
	// runs from) — the restack replays from gt's cached tip, not live HEAD.
	// Don't hand-commit to `baseBranch` while a run against it is in
	// progress; if you must, run `gt track <baseBranch> --force` again
	// immediately after so gt's cache catches up before the next `gt sync`.
	gtTrack(baseBranch, TRUNK_BRANCH, repoRoot);
	log(`Tracked feature branch "${baseBranch}" with Graphite (parent: ${TRUNK_BRANCH})`);

	try {
		for (let supercycle = 1; supercycle <= MAX_SUPERCYCLES; supercycle++) {
			log(`--- Supercycle ${supercycle}/${MAX_SUPERCYCLES} ---`);

			try {
				gtSync(repoRoot);
			} catch (err) {
				log(`gt sync failed: ${(err as Error).message}. Stopping for manual resolution.`);
				break;
			}

			let batch: ReturnType<typeof claimBatch>;
			try {
				batch = claimBatch(parentId, repoRoot, MAX_ITERATIONS);
			} catch (err) {
				log(`Claiming ready issues failed: ${(err as Error).message}`);
				break;
			}

			if (batch.length === 0) {
				log("No ready issues remain under this parent. Stopping.");
				break;
			}

			log(`Claimed ${batch.length} issue(s) to run in parallel this supercycle: ${batch.map((c) => c.id).join(", ")}`);

			// Sandboxed implement runs are the only thing that runs concurrently.
			// gt track/submit happen afterward, one at a time — gt's local repo
			// metadata isn't safe for concurrent writers the way bd's Dolt DB is.
			const settled = await Promise.allSettled(
				batch.map(async (issue) => {
					const branch = branchNameFor(issue);
					const parentBranch = resolveParentBranch(issue, baseBranch, repoRoot);
					const logFilePath = path.join(runDir, `implement-${issue.id}.log`);
					log(`[${issue.id}] Starting sandboxed implement run on branch ${branch} (stacked on ${parentBranch})`);

					let result: Awaited<ReturnType<typeof sandcastleRun>>;
					try {
						result = await sandcastleRun({
							cwd: repoRoot,
							name: issue.id,
							// We never resume/fork a session, and a transient capture failure
							// shouldn't revert an otherwise-successful run (bd close + commits
							// already landed by the time capture happens).
							agent: claudeCode("claude-sonnet-4-6", { captureSessions: false }),
							sandbox: docker({
								imageName: IMAGE_NAME,
								network: infra.networkName,
								mounts: [beadsMount],
								// bd's discovery walks up from cwd, which inside the sandbox is
								// the bind-mounted worktree (a different path than repoRoot) —
								// it never reaches the mounted .beads dir. Point it there directly.
								env: { BEADS_DIR: beadsMount.sandboxPath, TEST_DATABASE_URL: infra.testDatabaseUrlFor(issue.id) },
							}),
							branchStrategy: { type: "branch", branch, baseBranch: parentBranch },
							promptFile: path.join(repoRoot, ".sandcastle", "implement-prompt.md"),
							promptArgs: { ISSUE_ID: issue.id, PARENT_ID: parentId },
							maxIterations: 1,
							idleTimeoutSeconds: IDLE_TIMEOUT_SECONDS,
							logging: {
								type: "file",
								path: logFilePath,
								onAgentStreamEvent: (event) => {
									if (event.type === "text") log(`[${issue.id}] assistant: ${event.message.slice(0, 300)}`);
									else if (event.type === "toolCall") log(`[${issue.id}] tool: ${event.name} (${event.formattedArgs.slice(0, 100)})`);
								},
							},
						});
					} catch (err) {
						log(`[${issue.id}] sandbox run threw: ${(err as Error).message}. Reverting claim.`);
						revertClaim(issue.id, repoRoot, log);
						return null;
					}

					const closed = issueStatus(issue.id, repoRoot) === "closed";
					if (!closed) {
						log(`[${issue.id}] did not close the bd issue. Reverting claim.`);
						revertClaim(issue.id, repoRoot, log);
						return null;
					}
					if (result.commits.length === 0) {
						log(`[${issue.id}] closed the issue but made no commits. Reverting claim.`);
						revertClaim(issue.id, repoRoot, log);
						return null;
					}

					log(`[${issue.id}] completed with ${result.commits.length} commit(s).`);
					return { issue, branch, parentBranch };
				}),
			);

			for (const outcome of settled) {
				if (outcome.status !== "fulfilled" || outcome.value === null) continue;
				const { issue, branch, parentBranch } = outcome.value;
				log(`[${issue.id}] Tracking and submitting branch ${branch} (parent: ${parentBranch})...`);
				try {
					gtTrack(branch, parentBranch, repoRoot);
					const prUrl = gtSubmit(branch, repoRoot);
					log(`[${issue.id}] PR submitted: ${prUrl ?? "(no URL parsed from gt output)"}`);
				} catch (err) {
					// The implement run already succeeded (bd close + commits landed on
					// `branch`), so don't let it vanish into a closed issue with no PR:
					// reopen it the same way a failed implement attempt is reverted, so
					// a human sees it (and future claims skip it instead of silently
					// re-running the already-done implement step).
					log(`[${issue.id}] gt track/submit failed: ${(err as Error).message}. Branch ${branch} has unsubmitted commits — reverting claim for human follow-up.`);
					revertClaim(issue.id, repoRoot, log);
				}
			}
		}
	} finally {
		infra.teardown();
		log(`Run infra torn down: ${infra.networkName}`);
	}

	log("Orchestrator run finished.");
}

main().catch((err) => {
	console.error(err);
	process.exit(1);
});
