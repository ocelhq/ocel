import { existsSync, readFileSync } from "node:fs";

export function parseEnvFile(text) {
  const out = {};
  for (const raw of String(text ?? "").split("\n")) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const at = line.indexOf("=");
    if (at <= 0) continue;
    const name = line.slice(0, at).trim();
    const value = line.slice(at + 1).trim();
    out[name] = value.replace(/^(['"])(.*)\1$/, "$2");
  }
  return out;
}

export function loadEnvFile(path, env = process.env) {
  if (!existsSync(path)) return [];
  const parsed = parseEnvFile(readFileSync(path, "utf8"));
  const applied = [];
  for (const [name, value] of Object.entries(parsed)) {
    if (env[name]) continue;
    env[name] = value;
    applied.push(name);
  }
  return applied;
}
