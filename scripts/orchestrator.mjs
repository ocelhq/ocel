#!/usr/bin/env node
// Claims ready-for-agent bd issues under a given parent (epic/PRD) and
// implements them one batch at a time, each in its own git worktree, via the
// `claude` CLI. Git worktrees auto-share the main checkout's beads database
// (git common-dir discovery), so issue state never needs to be ferried in or
// out — a `bd` command run inside a worktree is visible from the main
// checkout immediately. See `.scratch/<parent-id>/orchestrator-runs/<timestamp>/`
// for per-run logs after a run.

import { spawn, spawnSync } from "node:child_process";
import { appendFileSync, mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";

// Max issues run concurrently (in separate worktrees) within one supercycle.
const MAX_ITERATIONS = 3;
// Safety cap on the number of re-claim batches, in case something keeps
// re-claiming without making progress.
const MAX_SUPERCYCLES = 20;
const IMPLEMENT_TIMEOUT_MS = 30 * 60 * 1000;
const WORKTREE_BASE_DIRNAME = "ocel-orchestrator-worktrees";
// Applied to an issue when its implement attempt fails, so future claims in
// this run skip it instead of retrying it forever.
const FAILED_LABEL = "orchestrator-failed";

function usageError(message) {
	console.error(message);
	console.error("Usage: node scripts/orchestrator.mjs <parent-issue-id>");
	process.exit(1);
}

function git(args, cwd) {
	const res = spawnSync("git", args, { cwd, encoding: "utf8" });
	if (res.status !== 0) {
		throw new Error(`git ${args.join(" ")} failed: ${res.stderr.trim()}`);
	}
	return res.stdout.trim();
}

function bd(args, cwd) {
	return spawnSync("bd", args, { cwd, encoding: "utf8" });
}

function remoteBranchExists(branch, cwd) {
	const res = spawnSync("git", ["ls-remote", "--exit-code", "--heads", "origin", branch], {
		cwd,
		encoding: "utf8",
	});
	return res.status === 0;
}

function localBranchExists(branch, cwd) {
	const res = spawnSync("git", ["rev-parse", "--verify", "--quiet", branch], { cwd, encoding: "utf8" });
	return res.status === 0;
}

function listExistingWorktrees(cwd) {
	const res = spawnSync("git", ["worktree", "list", "--porcelain"], { cwd, encoding: "utf8" });
	if (res.status !== 0) return [];

	const worktrees = [];
	let current = null;
	for (const line of res.stdout.split("\n")) {
		if (line.startsWith("worktree ")) {
			if (current) worktrees.push(current);
			current = { path: line.slice("worktree ".length).trim(), branch: null };
		} else if (line.startsWith("branch ") && current) {
			current.branch = line.slice("branch ".length).trim().replace(/^refs\/heads\//, "");
		}
	}
	if (current) worktrees.push(current);
	return worktrees;
}

// Creates a worktree for `branch`, or reuses one already there (branch and/or
// worktree directory) — a previous run may have left one behind after a
// failed implement call, and retrying should resume in it rather than error
// out because "git worktree add" refuses to recreate an existing branch/path.
// The worktree automatically shares the main checkout's beads database via
// git's common-dir discovery — no extra setup needed for `bd` to work in it.
function ensureWorktree(branch, worktreePath, baseBranch, repoRoot) {
	const existing = listExistingWorktrees(repoRoot).find((w) => w.branch === branch || w.path === worktreePath);
	if (existing) {
		return { worktreePath: existing.path, reused: true };
	}

	mkdirSync(path.dirname(worktreePath), { recursive: true });
	if (localBranchExists(branch, repoRoot)) {
		git(["worktree", "add", worktreePath, branch], repoRoot);
	} else {
		git(["worktree", "add", "-b", branch, worktreePath, baseBranch], repoRoot);
	}
	return { worktreePath, reused: false };
}

function slugify(text) {
	return text
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "")
		.slice(0, 50);
}

function branchNameFor(issue) {
	return `ocel/issue-${issue.id}-${slugify(issue.title)}`;
}

// Atomically claims the next unblocked, ready-for-agent bd issue under
// `parentId` (sets its assignee and status: in_progress), or returns null if
// none remain. Relies on bd's own blocker-aware ready-work semantics
// (`bd ready`), so there's no need to re-derive "## Blocked by" relationships
// ourselves — dependencies are native bd data (`bd dep`).
function claimNextReadyIssue(parentId, repoRoot) {
	const res = bd(
		["ready", "--claim", "--json", "--parent", parentId, "--label", "ready-for-agent", "--exclude-label", FAILED_LABEL],
		repoRoot,
	);
	if (res.status !== 0) {
		throw new Error(`bd ready --claim failed: ${res.stderr.trim()}`);
	}
	let issues;
	try {
		issues = JSON.parse(res.stdout);
	} catch {
		throw new Error(`bd ready --claim produced non-JSON output: ${res.stdout.slice(0, 2000)}`);
	}
	return issues[0] ?? null;
}

function claimBatch(parentId, repoRoot, maxCount) {
	const batch = [];
	for (let i = 0; i < maxCount; i++) {
		const issue = claimNextReadyIssue(parentId, repoRoot);
		if (!issue) break;
		batch.push(issue);
	}
	return batch;
}

// Reverts a failed attempt: reopens the issue, clears its assignee, and
// labels it so this run's future claims skip it instead of retrying forever.
function revertClaim(issueId, repoRoot, log) {
	const res = bd(["update", issueId, "--status=open", "--assignee=", "--add-label", FAILED_LABEL], repoRoot);
	if (res.status !== 0) {
		log(`Failed to revert claim on ${issueId}: ${res.stderr.trim()}`);
	}
}

// Reads the issue's current status straight from the shared beads database —
// safe to call from the main checkout even though the implement call ran in
// a worktree, since both see the same database.
function issueStatus(issueId, repoRoot) {
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

function buildImplementPrompt(issueId, branch, baseBranch) {
	return `# TASK

Fix issue ${issueId}

Run \`bd show ${issueId}\` to pull in the issue (title, description, acceptance criteria, notes). If it has a parent epic/PRD (the "parent" field in \`bd show ${issueId} --json\`), run \`bd show <parent-id>\` for that context too.

Only work on the issue specified.

You are already on branch ${branch} in a dedicated worktree. This worktree shares the same beads database as the main checkout (git worktrees auto-share bd's database via common-dir discovery), so any \`bd\` command you run here — labels, comments, closing the issue — is visible everywhere else immediately.

Make commits, run tests, and create a PR against ${baseBranch} (not main). Use the \`gh\` cli, which is installed, to do so.

# CONTEXT

# EXPLORATION

Explore the repo and fill your context window with relevant information that will allow you to complete the task.

Pay extra attention to test files that touch the relevant parts of the code.

# EXECUTION

If applicable, use RGR to complete the task.

1. RED: write one test
2. GREEN: write the implementation to pass that test
3. REPEAT until done
4. REFACTOR the code

# FEEDBACK LOOPS

Before committing, run the relevant typecheck and test commands for the part of the repo you touched, and ensure they pass.

# COMMIT

Make a git commit. The commit message must:

1. Have a concise descriptive title
2. Include task completed + PRD reference
3. Key decisions made
4. Files changed
5. Blockers or notes for next iteration

Keep it concise.

# THE ISSUE

If the task is complete, run \`bd close ${issueId} --reason="<one-line summary>"\`.

If the task is not complete, run \`bd comment ${issueId} "<what was done>"\` and leave its status as in_progress.

# FINAL RULES

ONLY WORK ON A SINGLE TASK.`;
}

function truncate(str, n) {
	return str.length > n ? `${str.slice(0, n)}…` : str;
}

function summarizeStreamEvent(event) {
	switch (event.type) {
		case "assistant": {
			const parts = [];
			for (const item of event.message?.content ?? []) {
				if (item.type === "text" && item.text) {
					parts.push(`assistant: ${truncate(item.text, 300)}`);
				} else if (item.type === "tool_use") {
					const detail = item.input?.command ?? item.input?.file_path ?? item.input?.description ?? "";
					parts.push(`tool: ${item.name}${detail ? ` (${truncate(String(detail), 100)})` : ""}`);
				}
			}
			return parts.length ? parts.join("\n") : null;
		}
		case "result":
			return `implement call finished: ${event.subtype} (cost $${event.total_cost_usd?.toFixed?.(4) ?? "?"}, ${event.num_turns} turns)`;
		case "rate_limit_event":
			return `rate limit status: ${event.rate_limit_info?.status}`;
		default:
			return null;
	}
}

// Streams the implement call live: prints a condensed one-line-per-event
// summary via onProgress, while writing the full raw stream-json event log
// to logFilePath (for `tail -f`-ing the exact tool calls/output).
function runClaudeImplement(prompt, cwd, logFilePath, onProgress) {
	return new Promise((resolve) => {
		const child = spawn(
			"claude",
			["-p", "--output-format", "stream-json", "--verbose", "--permission-mode", "bypassPermissions"],
			{ cwd, stdio: ["pipe", "pipe", "pipe"] },
		);

		let buffer = "";
		let stderr = "";
		let finalResult = null;
		let timedOut = false;

		const timer = setTimeout(() => {
			timedOut = true;
			child.kill("SIGKILL");
		}, IMPLEMENT_TIMEOUT_MS);

		child.stdout.on("data", (chunk) => {
			buffer += chunk.toString();
			let idx = buffer.indexOf("\n");
			while (idx !== -1) {
				const line = buffer.slice(0, idx);
				buffer = buffer.slice(idx + 1);
				idx = buffer.indexOf("\n");
				if (!line.trim()) continue;
				appendFileSync(logFilePath, `${line}\n`);
				let event;
				try {
					event = JSON.parse(line);
				} catch {
					continue;
				}
				if (event.type === "result") finalResult = event;
				const summary = summarizeStreamEvent(event);
				if (summary) onProgress(summary);
			}
		});

		child.stderr.on("data", (chunk) => {
			stderr += chunk.toString();
		});

		child.on("error", (error) => {
			clearTimeout(timer);
			resolve({ status: null, error, timedOut, finalResult, stderr });
		});

		child.on("close", (status) => {
			clearTimeout(timer);
			resolve({ status, error: null, timedOut, finalResult, stderr });
		});

		child.stdin.write(prompt);
		child.stdin.end();
	});
}

async function main() {
	const parentId = process.argv[2];
	if (!parentId) usageError("Missing <parent-issue-id> argument.");

	const repoRoot = git(["rev-parse", "--show-toplevel"], process.cwd());

	const parentCheck = bd(["show", parentId, "--json"], repoRoot);
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
	const runDir = path.join(repoRoot, ".scratch", parentId, "orchestrator-runs", runTimestamp);
	mkdirSync(runDir, { recursive: true });

	function log(msg) {
		const line = `[${new Date().toISOString()}] ${msg}`;
		console.log(line);
		appendFileSync(path.join(runDir, "run.log"), `${line}\n`);
	}

	const worktreeBase = path.resolve(repoRoot, "..", WORKTREE_BASE_DIRNAME);

	log(`Starting orchestrator run for parent "${parentId}" on base branch "${baseBranch}"`);

	for (let supercycle = 1; supercycle <= MAX_SUPERCYCLES; supercycle++) {
		log(`--- Supercycle ${supercycle}/${MAX_SUPERCYCLES} ---`);

		let batch;
		try {
			batch = claimBatch(parentId, repoRoot, MAX_ITERATIONS);
		} catch (err) {
			log(`Claiming ready issues failed: ${err.message}`);
			break;
		}
		writeFileSync(path.join(runDir, `claimed-${supercycle}.json`), JSON.stringify(batch, null, 2));

		if (batch.length === 0) {
			log("No ready issues remain under this parent. Stopping.");
			break;
		}

		log(`Claimed ${batch.length} issue(s) to run in parallel this supercycle: ${batch.map((c) => c.id).join(", ")}`);

		// Sequential setup: worktree creation touches shared git repo state, so
		// it runs one at a time. Only the (slow) implement calls themselves run
		// concurrently below.
		const prepared = [];
		for (const issue of batch) {
			const branch = branchNameFor(issue);

			let worktreePath;
			try {
				const desiredPath = path.join(worktreeBase, branch);
				const result = ensureWorktree(branch, desiredPath, baseBranch, repoRoot);
				worktreePath = result.worktreePath;
				if (result.reused) {
					log(`[${issue.id}] Reusing existing branch/worktree at ${worktreePath} (left over from a previous attempt).`);
				}
			} catch (err) {
				log(`Failed to create worktree for "${branch}": ${err.message}`);
				revertClaim(issue.id, repoRoot, log);
				continue;
			}

			prepared.push({ issue, branch, worktreePath });
		}

		if (prepared.length === 0) {
			log("No candidates survived setup this supercycle.");
			continue;
		}

		await Promise.allSettled(
			prepared.map(async ({ issue, branch, worktreePath }) => {
				const implementPrompt = buildImplementPrompt(issue.id, branch, baseBranch);

				const implementLogPath = path.join(runDir, `implement-${issue.id}.log`);
				log(`[${issue.id}] Running implement call in ${worktreePath} (timeout ${IMPLEMENT_TIMEOUT_MS / 60000}min)`);
				log(`[${issue.id}] Follow along: tail -f ${implementLogPath} for the full raw event stream.`);

				const implementRes = await runClaudeImplement(implementPrompt, worktreePath, implementLogPath, (summary) =>
					log(`[${issue.id}] ${summary}`),
				);
				if (implementRes.stderr) {
					appendFileSync(implementLogPath, `\n--- stderr ---\n${implementRes.stderr}\n`);
				}
				if (implementRes.finalResult) {
					log(
						`[${issue.id}] Implement result: ${implementRes.finalResult.subtype} (cost $${implementRes.finalResult.total_cost_usd?.toFixed?.(4) ?? "?"})`,
					);
				}

				const timedOut = implementRes.timedOut;
				const closed = issueStatus(issue.id, repoRoot) === "closed";
				const succeeded = !timedOut && !implementRes.error && implementRes.status === 0 && closed;

				if (succeeded) {
					log(`[${issue.id}] completed successfully.`);
				} else {
					const reason = timedOut
						? "timed out"
						: implementRes.error
							? `spawn error: ${implementRes.error.message}`
							: implementRes.status !== 0
								? `exited with status ${implementRes.status}`
								: "did not close the bd issue";
					log(`[${issue.id}] did not complete (${reason}). Reverting claim, worktree left at ${worktreePath} for inspection.`);
					revertClaim(issue.id, repoRoot, log);
				}
			}),
		);
	}

	log("Orchestrator run finished.");
}

main().catch((err) => {
	console.error(err);
	process.exit(1);
});
