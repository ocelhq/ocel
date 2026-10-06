import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

const VERSION = readFileSync(join(root, "VERSION"), "utf8").trim();

const SCHEMA_OUT = join(root, "www", "public", "schema", VERSION, "ocel.schema.json");
const TYPES_OUT = join(root, "packages", "ocel", "src", "generated", "config.ts");

const read = (path) => JSON.parse(readFileSync(path, "utf8"));

const MESSAGE_SCHEMAS_OUT = join(root, "www", "public", "schema", VERSION, "cli");
const EMBEDDED_SCHEMAS_OUT = join(root, "cli", "internal", "outputschema", "schemas");
const MESSAGE_SCHEMA_ORIGIN = `https://ocel.dev/schema/${VERSION}/cli`;
const BUNDLE_SUFFIX = ".jsonschema.bundle.json";

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

function edgeFragments(dir = join(root, "platform")) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      return entry.name === "node_modules" || entry.name.startsWith(".") ? [] : edgeFragments(path);
    }
    return entry.name === "schema.edge.json" ? [read(path)] : [];
  });
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

function servedEdges(fragments, selector) {
  const options = new Map(edgeFragments().map((fragment) => [fragment.id, fragment.options]));
  const ids = [...new Set(fragments.flatMap((fragment) => fragment.edges))].sort();
  for (const id of ids) {
    if (!options.has(id)) {
      throw new Error(
        `a provider fronts deployments with the ${id} edge, and no schema.edge.json under platform/ declares its options`,
      );
    }
  }
  for (const id of options.keys()) {
    if (!ids.includes(id)) {
      throw new Error(
        `platform/ declares options for the ${id} edge, and no provider fronts deployments with it`,
      );
    }
  }
  return keyed(
    selector,
    ids.map((id) => [id, options.get(id)]),
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
      edge: servedEdges(fragments, edge),
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
  return {
    provider: { ...selection(provider), required: requiredText(provider) },
    edge: selection(edge),
    dns: selection(dns),
  };
}

function requiredText(selector) {
  const acceptsText = (node) =>
    node?.type === "string" || (node?.oneOf ?? []).some((one) => one.type === "string");
  return Object.fromEntries(
    Object.entries(selector.oneOf[1].properties)
      .map(([id, options]) => [
        id,
        (options.required ?? [])
          .filter((name) => acceptsText(options.properties?.[name]))
          .map((name) => ({ name, doc: options.properties[name].description ?? "" })),
      ])
      .filter(([, required]) => required.length > 0),
  );
}

const RESERVED = new Set(["OcelConfig"]);

class Emitter {
  constructor() {
    this.interfaces = new Map();
  }

  type(node, indent = "") {
    if (node === false) return "never";
    if (node.allOf) return this.type(node.allOf[0], indent);
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
    const keys = node.oneOf.filter(soleKey).map((one) => Object.keys(one.properties)[0]);
    const member = (one, at) => (soleKey(one) ? this.keyed(one, keys, at) : this.type(one, at));
    if (!node.title || RESERVED.has(node.title)) {
      return node.oneOf.map((one) => member(one, indent)).join(" | ");
    }
    if (!this.interfaces.has(node.title)) {
      this.interfaces.set(node.title, "");
      const union = node.oneOf.map((one) => member(one, "")).join("\n  | ");
      this.interfaces.set(node.title, `${doc(node)}export type ${node.title} =\n  | ${union};\n`);
    }
    return node.title;
  }

  keyed(node, keys, indent) {
    const inner = `${indent}  `;
    const [key] = Object.keys(node.properties);
    const property = node.properties[key];
    const lines = property.description ? [`${inner}/** ${property.description} */`] : [];
    lines.push(`${inner}${quoted(key)}: ${this.type(property, inner)};`);
    for (const other of keys.filter((each) => each !== key)) {
      lines.push(`${inner}${quoted(other)}?: never;`);
    }
    return `{\n${lines.join("\n")}\n${indent}}`;
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

function soleKey(node) {
  return (
    !node.title &&
    node.type === "object" &&
    node.additionalProperties === false &&
    Object.keys(node.properties ?? {}).length === 1 &&
    node.required?.length === 1
  );
}

function prefixed(pattern) {
  const spelled = /^\^((?:[^\\^$.|?*+()[\]{}`]|\\[$.|?*+()[\]{}^\\])+)/.exec(pattern ?? "")?.[1];
  if (!spelled) return "string";
  const prefix = spelled.replace(/\\(.)/g, "$1").replace(/\\/g, "\\\\").replace(/\$\{/g, "\\${");
  return `\`${prefix}\${string}\``;
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

function generateMessageSchemas() {
  const scratch = mkdtempSync(join(tmpdir(), "ocel-message-schemas-"));
  try {
    execFileSync(
      "pnpm",
      ["exec", "buf", "generate", "--template", "proto/buf.gen.schema.yaml", "--output", scratch],
      { cwd: root, stdio: "inherit" },
    );
    return readdirSync(scratch)
      .filter((file) => file.endsWith(BUNDLE_SUFFIX))
      .sort()
      .map((file) => {
        const name = file.slice(0, -BUNDLE_SUFFIX.length);
        const bundle = read(join(scratch, file));
        refuseNonECMAScriptPatterns(name, bundle);
        return {
          file: `${name}.schema.json`,
          schema: { ...bundle, $id: `${MESSAGE_SCHEMA_ORIGIN}/${name}.schema.json` },
        };
      });
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}

function refuseNonECMAScriptPatterns(name, node) {
  if (!node || typeof node !== "object") return;
  if (Array.isArray(node)) {
    for (const item of node) refuseNonECMAScriptPatterns(name, item);
    return;
  }
  if (typeof node.pattern === "string") {
    try {
      new RegExp(node.pattern, "u");
    } catch (error) {
      throw new Error(
        `${name} has a pattern that is not ECMA-262: ${node.pattern} (${error.message})`,
      );
    }
  }
  for (const value of Object.values(node)) refuseNonECMAScriptPatterns(name, value);
}

function writeMessageSchemas(generated) {
  const outputs = [MESSAGE_SCHEMAS_OUT, EMBEDDED_SCHEMAS_OUT];
  for (const out of outputs) {
    rmSync(out, { recursive: true, force: true });
    mkdirSync(out, { recursive: true });
  }
  for (const { file, schema: document } of generated) {
    const text = `${JSON.stringify(document, null, 2)}\n`;
    for (const out of outputs) writeFileSync(join(out, file), text);
  }
}

const merged = schema();
const messageSchemas = generateMessageSchemas();
mkdirSync(dirname(SCHEMA_OUT), { recursive: true });
writeFileSync(SCHEMA_OUT, `${JSON.stringify(merged, null, 2)}\n`);
mkdirSync(dirname(TYPES_OUT), { recursive: true });
writeFileSync(TYPES_OUT, types(merged));
writeFileSync(SELECTORS_OUT, `${JSON.stringify(selectors(merged), null, 2)}\n`);
writeMessageSchemas(messageSchemas);
execFileSync("pnpm", ["exec", "biome", "format", "--write", SCHEMA_OUT, TYPES_OUT, SELECTORS_OUT], {
  cwd: root,
  stdio: "inherit",
});
