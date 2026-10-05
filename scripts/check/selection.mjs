import derived from "./derived.json" with { type: "json" };

const WORKSPACE_FILES = ["go.work", "go.work.sum", ".golangci.yml"];

const MODULE_WHOSE_TESTS_READ_FILE = new Map([["www/content/docs/telemetry.mdx", "cli"]]);

export function touches(changed, dir) {
  return changed.some((file) => file.startsWith(`${dir}/`));
}

function matcher(pattern) {
  if (pattern.endsWith("/")) return (file) => file.startsWith(pattern);
  const source = pattern
    .replace(/[.+^${}()|[\]\\]/g, "\\$&")
    .replace(/\*\*/g, "\0")
    .replace(/\*/g, "[^/]*")
    .replace(/\0/g, ".*");
  const whole = new RegExp(`^${source}$`);
  return (file) => whole.test(file) || file.startsWith(`${pattern}/`);
}

const derivedMatchers = [...derived.sources, ...derived.generated].map(matcher);

export function derivesFrom(changed) {
  return changed.some((file) => derivedMatchers.some((matches) => matches(file)));
}

function moduleOf(file, modules) {
  const readBy = MODULE_WHOSE_TESTS_READ_FILE.get(file);
  if (readBy) return modules.find((module) => module.dir === readBy);
  return modules
    .filter((module) => file.startsWith(`${module.dir}/`))
    .sort((a, b) => b.dir.length - a.dir.length)[0];
}

function byDir(a, b) {
  return a.dir < b.dir ? -1 : a.dir > b.dir ? 1 : 0;
}

export function selectGoModules(changed, modules) {
  const workspaceFile = changed.find((file) => WORKSPACE_FILES.includes(file));
  if (workspaceFile) {
    return modules.map(({ dir }) => ({ dir, reason: `changed ${workspaceFile}` })).sort(byDir);
  }

  const selected = new Map();
  const changedIn = new Map();
  for (const file of changed) {
    const module = moduleOf(file, modules);
    if (module) changedIn.set(module.dir, [...(changedIn.get(module.dir) ?? []), file]);
  }
  for (const [dir, files] of changedIn) {
    const more = files.length > 1 ? ` and ${files.length - 1} more` : "";
    selected.set(dir, `changed ${files[0]}${more}`);
  }

  const dirOf = new Map(modules.map((module) => [module.path, module.dir]));
  for (;;) {
    const wave = [];
    for (const module of modules) {
      if (selected.has(module.dir)) continue;
      const required = module.requires
        .map((path) => dirOf.get(path))
        .find((dir) => selected.has(dir));
      if (required) wave.push([module.dir, `requires ${required}`]);
    }
    if (wave.length === 0) break;
    for (const [dir, reason] of wave) selected.set(dir, reason);
  }

  return [...selected].map(([dir, reason]) => ({ dir, reason })).sort(byDir);
}

export function selectSetup(selectedDirs, modules, setupDirs) {
  const byPath = new Map(modules.map((module) => [module.path, module]));
  const needed = new Set();
  const pending = modules.filter((module) => selectedDirs.includes(module.dir));
  while (pending.length > 0) {
    const module = pending.pop();
    if (needed.has(module.dir)) continue;
    needed.add(module.dir);
    for (const path of module.requires) {
      const required = byPath.get(path);
      if (required) pending.push(required);
    }
  }
  return setupDirs.filter((dir) => needed.has(moduleOf(`${dir}/`, modules)?.dir));
}
