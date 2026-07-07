// Claims ready-for-agent bd issues under a given parent (epic/PRD) and
// implements them one batch at a time, each in its own Docker sandbox (via
// sandcastle) on its own branch. Agents never see a GitHub token — once a
// sandboxed run closes its bd issue, the host pushes the branch and opens
// the PR. See `.scratch/<parent-id>/orchestrator-runs/<timestamp>/` for
// per-run logs after a run.

import { spawnSync } from "node:child_process";
import { appendFileSync, mkdirSync } from "node:fs";
import path from "node:path";
import { claudeCode, run as sandcastleRun } from "@ai-hero/sandcastle";
import { docker } from "@ai-hero/sandcastle/sandboxes/docker";
import { claimBatch, issueStatus, revertClaim } from "./bd.ts";
import { branchNameFor, git, pushAndOpenPr, remoteBranchExists } from "./git.ts";
import { setupRunInfra } from "./infra.ts";

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

	const infra = setupRunInfra(runId);
	log(`Run infra ready: network ${infra.networkName}, Postgres sidecar ${infra.postgresContainerName}`);

	const beadsMount = { hostPath: path.join(repoRoot, ".beads"), sandboxPath: path.join(repoRoot, ".beads") };

	try {
		for (let supercycle = 1; supercycle <= MAX_SUPERCYCLES; supercycle++) {
			log(`--- Supercycle ${supercycle}/${MAX_SUPERCYCLES} ---`);

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

			await Promise.allSettled(
				batch.map(async (issue) => {
					const branch = branchNameFor(issue);
					const logFilePath = path.join(runDir, `implement-${issue.id}.log`);
					log(`[${issue.id}] Starting sandboxed implement run on branch ${branch}`);

					let result: Awaited<ReturnType<typeof sandcastleRun>>;
					try {
						result = await sandcastleRun({
							cwd: repoRoot,
							name: issue.id,
							agent: claudeCode("claude-sonnet-4-6"),
							sandbox: docker({
								imageName: IMAGE_NAME,
								network: infra.networkName,
								mounts: [beadsMount],
								env: { TEST_DATABASE_URL: infra.testDatabaseUrlFor(issue.id) },
							}),
							branchStrategy: { type: "branch", branch },
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
						return;
					}

					const closed = issueStatus(issue.id, repoRoot) === "closed";
					if (!closed) {
						log(`[${issue.id}] did not close the bd issue. Reverting claim.`);
						revertClaim(issue.id, repoRoot, log);
						return;
					}
					if (result.commits.length === 0) {
						log(`[${issue.id}] closed the issue but made no commits. Reverting claim.`);
						revertClaim(issue.id, repoRoot, log);
						return;
					}

					log(`[${issue.id}] completed with ${result.commits.length} commit(s). Pushing and opening PR...`);
					try {
						const prUrl = pushAndOpenPr(branch, baseBranch, `${issue.id}: ${issue.title}`, `Closes ${issue.id}.`, repoRoot);
						log(`[${issue.id}] PR opened: ${prUrl}`);
					} catch (err) {
						log(`[${issue.id}] push/PR failed: ${(err as Error).message}`);
					}
				}),
			);
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
