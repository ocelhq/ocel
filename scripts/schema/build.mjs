import { execFileSync } from "node:child_process";
import { mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

const VERSION = readFileSync(join(root, "VERSION"), "utf8").trim();

const SCHEMA_OUT = join(root, "www", "public", "schema", VERSION, "ocel.schema.json");
const TYPES_OUT = join(root, "packages", "ocel", "src", "generated", "config.ts");

const read = (path) => JSON.parse(readFileSync(path, "utf8"));

const SCHEMA_URL = `https://ocel.dev/schema/${VERSION}/ocel.schema.json`;

const SELECTORS_OUT = join(root, "pkg", "configdoc", "selectors.json");

function providerFragments() {
  const platform = join(root, "platform");
  return readdirSync(platform, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => join(platform, entry.name, "provider", "schema.provider.json"))
    .filter((path) => {
      try {
        readFileSync(path);
        return true;
      } catch {
        return false;
      }
    })
    .map(read)
    .map(qualified)
    .sort((a, b) => a.id.localeCompare(b.id));
}

function qualified(fragment) {
  const prefix = fragment.id[0].toUpperCase() + fragment.id.slice(1);
  const rename = (node) => {
    if (!node || typeof node !== "object") return;
    if (Array.isArray(node)) {
      for (const item of node) rename(item);
      return;
    }
    if (typeof node.title === "string" && !node.title.startsWith(prefix)) {
      node.title = prefix + node.title;
    }
    for (const value of Object.values(node)) rename(value);
  };
  rename(fragment.options);
  return fragment;
}

function keyed(selector, entries) {
  const [spelled, object] = selector.oneOf;
  const shorthand = entries.filter(([, options]) => !options.required?.length).map(([id]) => id);
  return {
    ...selector,
    oneOf: [
      { ...spelled, enum: shorthand },
      { ...object, additionalProperties: false, properties: Object.fromEntries(entries) },
    ],
  };
}

function servedBy(fragments, field, selector) {
  const ids = [...new Set(fragments.flatMap((fragment) => fragment[field]))].sort();
  const options = selector.oneOf[1].additionalProperties;
  return keyed(
    selector,
    ids.map((id) => [id, options]),
  );
}

function schema() {
  const core = read(join(root, "pkg", "configdoc", "schema.core.json"));
  const fragments = providerFragments();
  const { provider, edge, dns } = core.properties;
  return {
    $id: SCHEMA_URL,
    ...core,
    properties: {
      ...core.properties,
      provider: keyed(
        provider,
        fragments.map((fragment) => [fragment.id, fragment.options]),
      ),
      edge: servedBy(fragments, "edges", edge),
      dns: servedBy(fragments, "dns", dns),
    },
  };
}

function selectors(merged) {
  const selection = (selector) => ({
    ids: Object.keys(selector.oneOf[1].properties),
    shorthand: selector.oneOf[0].enum,
  });
  const { provider, edge, dns } = merged.properties;
  return { provider: selection(provider), edge: selection(edge), dns: selection(dns) };
}

const RESERVED = new Set(["OcelConfig"]);

class Emitter {
  constructor() {
    this.interfaces = new Map();
  }

  type(node, indent = "") {
    if (node.const !== undefined) return JSON.stringify(node.const);
    if (node.enum) return node.enum.map((value) => JSON.stringify(value)).join(" | ");
    if (node.oneOf) return this.union(node, indent);
    switch (node.type) {
      case "string":
        return prefixed(node.pattern);
      case "boolean":
        return "boolean";
      case "integer":
      case "number":
        return "number";
      case "array":
        return `${this.wrapped(node.items, indent)}[]`;
      case "object":
        return this.object(node, indent);
      default:
        return "unknown";
    }
  }

  wrapped(node, indent) {
    const rendered = this.type(node, indent);
    return rendered.includes(" | ") ? `(${rendered})` : rendered;
  }

  union(node, indent) {
    if (!node.title || RESERVED.has(node.title)) {
      return node.oneOf.map((one) => this.type(one, indent)).join(" | ");
    }
    if (!this.interfaces.has(node.title)) {
      this.interfaces.set(node.title, "");
      const union = node.oneOf.map((one) => this.type(one, "")).join("\n  | ");
      this.interfaces.set(node.title, `${doc(node)}export type ${node.title} =\n  | ${union};\n`);
    }
    return node.title;
  }

  exclusive(node, indent) {
    const inner = `${indent}  `;
    const ids = Object.keys(node.properties);
    return ids
      .map((id) => {
        const lines = ids.map((other) =>
          other === id
            ? `${inner}${quoted(id)}: ${this.type(node.properties[id], inner)};`
            : `${inner}${quoted(other)}?: never;`,
        );
        return `{\n${lines.join("\n")}\n${indent}}`;
      })
      .join(" | ");
  }

  object(node, indent) {
    if (node.maxProperties === 1 && node.properties) return this.exclusive(node, indent);
    if (
      node.properties &&
      Object.keys(node.properties).length === 0 &&
      node.additionalProperties === false
    ) {
      if (!node.title) return "Record<string, never>";
      if (!this.interfaces.has(node.title)) {
        this.interfaces.set(
          node.title,
          `${doc(node)}export type ${node.title} = Record<string, never>;\n`,
        );
      }
      return node.title;
    }
    if (!node.properties) {
      const values = node.additionalProperties;
      return values && values !== true
        ? `Record<string, ${this.type(values, indent)}>`
        : "Record<string, unknown>";
    }
    if (node.title && !RESERVED.has(node.title)) {
      this.declare(node.title, node);
      return node.title;
    }
    return this.body(node, indent);
  }

  body(node, indent) {
    const inner = `${indent}  `;
    const lines = [];
    for (const [key, property] of Object.entries(node.properties)) {
      if (property.description) {
        lines.push(`${inner}/** ${property.description} */`);
      }
      const optional = (node.required ?? []).includes(key) ? "" : "?";
      lines.push(`${inner}${quoted(key)}${optional}: ${this.type(property, inner)};`);
    }
    return `{\n${lines.join("\n")}\n${indent}}`;
  }

  declare(name, node) {
    if (this.interfaces.has(name)) return;
    this.interfaces.set(name, "");
    this.interfaces.set(name, `${doc(node)}export interface ${name} ${this.body(node, "")}\n`);
  }

  render() {
    return [...this.interfaces.values()].join("\n");
  }
}

function prefixed(pattern) {
  const prefix = /^\^([^\\^$.|?*+()[\]{}`]+)/.exec(pattern ?? "")?.[1];
  return prefix ? `\`${prefix}\${string}\`` : "string";
}

function doc(node) {
  return node.description ? `/** ${node.description} */\n` : "";
}

function quoted(key) {
  return /^[A-Za-z_$][A-Za-z0-9_$]*$/.test(key) ? key : JSON.stringify(key);
}

function types(merged) {
  const emitter = new Emitter();
  const config = emitter.body(merged, "");

  const header = [
    "// generated by scripts/schema/build.mjs from the committed JSON Schema; do not edit",
    "",
    `/** The project configuration \`ocel deploy\` reads, whether written as ${"`ocel.json`"}, as ${"`ocel.yaml`"} or as ${"`ocel.config.ts`"}. */`,
    `export interface OcelConfig ${config}`,
    "",
  ].join("\n");

  return `${header}\n${emitter.render()}`;
}

const merged = schema();
mkdirSync(dirname(SCHEMA_OUT), { recursive: true });
writeFileSync(SCHEMA_OUT, `${JSON.stringify(merged, null, 2)}\n`);
mkdirSync(dirname(TYPES_OUT), { recursive: true });
writeFileSync(TYPES_OUT, types(merged));
writeFileSync(SELECTORS_OUT, `${JSON.stringify(selectors(merged), null, 2)}\n`);
execFileSync("pnpm", ["exec", "biome", "format", "--write", SCHEMA_OUT, TYPES_OUT, SELECTORS_OUT], {
  cwd: root,
  stdio: "inherit",
});
