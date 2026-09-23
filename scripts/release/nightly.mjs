#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parse, pep440 } from "./version.mjs";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");

const DAY = 24 * 60 * 60 * 1000;

function parsed(tag) {
  if (!tag.startsWith("v")) return undefined;
  try {
    return { tag, ...parse(tag.slice(1)) };
  } catch {
    return undefined;
  }
}

function triple(base) {
  return base.split(".").map(Number);
}

function compareBase(a, b) {
  const [x, y] = [triple(a), triple(b)];
  return x[0] - y[0] || x[1] - y[1] || x[2] - y[2];
}

export function stamp(date) {
  return date.toISOString().slice(0, 10).replaceAll("-", "");
}

export function base(tags, version) {
  const stable = tags
    .map(parsed)
    .filter((tag) => tag?.channel === "stable")
    .map((tag) => tag.base);
  const current = parse(version);
  if (current.channel === "stable") stable.push(current.base);
  if (stable.length === 0) return current.base;
  const [major, minor, patch] = triple(stable.sort(compareBase).at(-1));
  return `${major}.${minor}.${patch + 1}`;
}

export function nightlyVersion({ tags, version, date, sha }) {
  if (!/^[0-9a-f]{40}$/.test(sha)) throw new Error(`${JSON.stringify(sha)} is no full commit sha`);
  return `${base(tags, version)}-0.nightly.${stamp(date)}.g${sha.slice(0, 7)}`;
}

export function nightlies(tags) {
  return tags.map(parsed).filter((tag) => tag?.channel === "nightly");
}

export function plan({ tags, version, date, sha }) {
  const short = sha.slice(0, 7);
  const built = nightlies(tags).find((tag) => tag.sha === short);
  if (built) return { skip: `${short} is already released as ${built.tag}` };
  const next = nightlyVersion({ tags, version, date, sha });
  const collision = nightlies(tags).find((tag) => pep440(tag.tag.slice(1)) === pep440(next));
  if (collision) {
    return {
      skip: `${collision.tag} already took PyPI's ${pep440(next)}, which ${short} would publish as too`,
    };
  }
  return { version: next };
}

export function stale(tags, now, days) {
  const cutoff = stamp(new Date(now.getTime() - days * DAY));
  return nightlies(tags)
    .filter((tag) => tag.date < cutoff)
    .map((tag) => tag.tag);
}

function git(...args) {
  return execFileSync("git", args, { cwd: REPO_ROOT, encoding: "utf8" }).trim();
}

function unchangedSinceLastNightly(sha) {
  const [latest] = git(
    "for-each-ref",
    "--sort=-creatordate",
    "--format=%(refname:short)",
    "refs/tags/v*",
  )
    .split("\n")
    .filter((tag) => parsed(tag)?.channel === "nightly");
  if (!latest) return undefined;
  const source = git("rev-parse", `${latest}^{commit}^`);
  try {
    git("diff", "--quiet", source, sha);
  } catch {
    return undefined;
  }
  return `nothing changed since ${latest}, built from ${source.slice(0, 7)}`;
}

function main() {
  const [command, argument] = process.argv.slice(2);
  switch (command) {
    case "plan": {
      if (!argument) throw new Error("usage: nightly.mjs plan <sha>");
      const sha = git("rev-parse", "--verify", `${argument}^{commit}`);
      const tags = git("tag", "--list", "v*").split("\n").filter(Boolean);
      const version = readFileSync(join(REPO_ROOT, "VERSION"), "utf8").trim();
      let outcome = plan({ tags, version, date: new Date(), sha });
      if (outcome.version) {
        const unchanged = unchangedSinceLastNightly(sha);
        if (unchanged) outcome = { skip: unchanged };
      }
      for (const [key, value] of Object.entries(outcome)) console.log(`${key}=${value}`);
      break;
    }
    case "stale": {
      const days = Number(argument);
      if (!Number.isInteger(days) || days < 1)
        throw new Error("usage: nightly.mjs stale <days> < tags");
      const tags = readFileSync(0, "utf8")
        .split("\n")
        .map((line) => line.trim())
        .filter(Boolean);
      for (const tag of stale(tags, new Date(), days)) console.log(tag);
      break;
    }
    default:
      throw new Error("usage: nightly.mjs plan <sha> | stale <days>");
  }
}

if (import.meta.main) {
  try {
    main();
  } catch (error) {
    console.error(error.message);
    process.exit(1);
  }
}
