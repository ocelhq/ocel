import { execFileSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

const DOCS_DIR = join(root, "www", "content", "docs", "cli");
const PROSE_DIR = join(root, "www", "content", "cli");
const DOCS_URL = "/docs/cli";
const GLOBAL_FLAGS_URL = `${DOCS_URL}#global-flags`;

const RESULT = "COMMAND_OUTPUT_RESULT";
const RUN_EVENTS = "COMMAND_OUTPUT_RUN_EVENTS";

function escapeText(text) {
  return text
    .split(/(`[^`]*`)/)
    .map((part, i) =>
      i % 2 === 1
        ? part
        : part
            .replace(/\\/g, "\\\\")
            .replace(/[{}*]/g, (char) => `\\${char}`)
            .replace(/</g, "&lt;")
            .replace(/>/g, "&gt;")
            .replace(
              /(^|[\s(])(--[a-z][a-z0-9-]*(?:=[^\s,;)]*[^\s,;.)])?|-[a-zA-Z](?=[\s,;.)]|$))/gm,
              "$1`$2`",
            ),
    )
    .join("");
}

function escapeCell(text) {
  return escapeText(text).replace(/\|/g, "\\|");
}

function codeCell(text) {
  return `\`${text.replace(/\|/g, "\\|")}\``;
}

function sentence(text) {
  return /[.!?]$/.test(text) ? text : `${text}.`;
}

function frontmatter(title, description) {
  return `---\ntitle: ${JSON.stringify(title)}\ndescription: ${JSON.stringify(description)}\n---`;
}

function renderLong(command) {
  const paragraphs = command.long.split(/\n\s*\n/).filter((paragraph) => paragraph.trim() !== "");
  if (paragraphs[0]?.trim() === sentence(command.short)) paragraphs.shift();
  return paragraphs.map((paragraph) => {
    const lines = paragraph.split("\n");
    if (lines.every((line) => /^ {2,}\S/.test(line))) {
      const indent = Math.min(...lines.map((line) => line.match(/^ */)[0].length));
      return `\`\`\`bash\n${lines.map((line) => line.slice(indent)).join("\n")}\n\`\`\``;
    }
    return escapeText(lines.map((line) => line.trim()).join("\n"));
  });
}

function pageURL(path) {
  return `${DOCS_URL}/${path.split(" ").join("/")}`;
}

function spellFlag(flag) {
  return flag.shorthand ? `-${flag.shorthand}, --${flag.name}` : `--${flag.name}`;
}

function flagRow(flag) {
  const fallback = flag.default === "" || flag.default === "[]" ? "" : codeCell(flag.default);
  return `| ${codeCell(spellFlag(flag))} | ${codeCell(flag.type)} | ${fallback} | ${escapeCell(flag.description)} |`;
}

const FLAG_HEADER = ["| Flag | Type | Default | Description |", "| --- | --- | --- | --- |"];

function spellArgument(argument) {
  const name = argument.required ? `<${argument.name}>` : `[${argument.name}]`;
  return argument.variadic ? `${name}...` : name;
}

function renderArguments(command) {
  if (command.arguments.length === 0) return [];
  return [
    "## Arguments",
    [
      "| Argument | Takes |",
      "| --- | --- |",
      ...command.arguments.map((argument) => {
        const takes = argument.required ? "Required" : "Optional";
        const repeats = argument.variadic ? ", and repeatable" : "";
        return `| ${codeCell(spellArgument(argument))} | ${takes}${repeats} |`;
      }),
    ].join("\n"),
  ];
}

function renderFlags(command) {
  const own = command.flags.filter((flag) => !flag.persistent);
  if (own.length === 0) return [];
  return [
    "## Flags",
    [...FLAG_HEADER, ...own.map(flagRow)].join("\n"),
    `Every command also takes the [global flags](${GLOBAL_FLAGS_URL}).`,
  ];
}

function renderSubcommands(subcommands) {
  if (subcommands.length === 0) return [];
  return [
    "## Commands",
    [
      "| Command | What it does |",
      "| --- | --- |",
      ...subcommands.map(
        (sub) => `| [\`ocel ${sub.path}\`](${pageURL(sub.path)}) | ${escapeCell(sub.short)} |`,
      ),
    ].join("\n"),
  ];
}

function hasFlag(command, name) {
  return command.flags.some((flag) => flag.name === name);
}

function renderBehaviour(command, output) {
  const lines = [command.mutates ? "- Changes state." : "- Read-only: it changes nothing."];
  if (command.confirms) {
    const ways = [];
    if (hasFlag(command, "yes")) ways.push("`--yes` consents in advance");
    if (hasFlag(command, "dry")) ways.push("`--dry` prints what it would change and stops");
    lines.push(`- Asks before it changes anything: ${ways.join(", and ")}.`);
  }
  if (output === RESULT) {
    lines.push(
      `- Prints data on stdout. Under \`--json\` it prints one result envelope, whose JSON Schema \`ocel schema ${command.path}\` prints.`,
    );
  } else if (output === RUN_EVENTS) {
    lines.push(
      `- Under \`--json\` it prints its run events as NDJSON, one per line, and \`ocel schema ${command.path}\` prints the JSON Schema of one line.`,
    );
  } else if (command.prints_data) {
    lines.push("- Prints data on stdout.");
  }
  return ["## Behaviour", lines.join("\n")];
}

export function renderPage(command, { output, subcommands, prose }) {
  const sections = [
    frontmatter(`ocel ${command.path}`, sentence(command.short)),
    `\`\`\`bash\n${command.usage}\n\`\`\``,
  ];
  if (command.aliases.length > 0) {
    const parent = command.path.split(" ").slice(0, -1);
    const aliases = command.aliases.map((alias) => `\`ocel ${[...parent, alias].join(" ")}\``);
    sections.push(`Also spelled ${aliases.join(" or ")}.`);
  }
  sections.push(...(prose ? [`<include>${prose}</include>`] : renderLong(command)));
  sections.push(...renderSubcommands(subcommands));
  const describesItself = subcommands.length === 0 || output !== undefined;
  if (describesItself) {
    sections.push(...renderArguments(command), ...renderFlags(command));
    sections.push(...renderBehaviour(command, output));
  }
  return `${sections.join("\n\n")}\n`;
}

function topLevel(commands) {
  return commands.filter((command) => !command.path.includes(" "));
}

export function renderIndex(commands, { prose } = {}) {
  const globals = commands[0]?.flags.filter((flag) => flag.persistent) ?? [];
  const sections = [
    frontmatter("CLI", "The ocel binary."),
    "```bash\nocel --help\n```",
    ...(prose ? [`<include>${prose}</include>`] : []),
    ...renderSubcommands(topLevel(commands)),
    "## Machine output",
    "Under `--json`, a command that returns data prints one result envelope, and a command that runs prints its run events as NDJSON, one per line. `ocel schema` lists every command that has a schema, and `ocel schema <command>` prints its JSON Schema.",
    "## Global flags",
    "Every command takes these.",
    [...FLAG_HEADER, ...globals.map(flagRow)].join("\n"),
  ];
  return `${sections.join("\n\n")}\n`;
}

function childrenOf(commands, path) {
  const depth = path.split(" ").length + 1;
  return commands.filter(
    (command) => command.path.startsWith(`${path} `) && command.path.split(" ").length === depth,
  );
}

function pageFile(commands, path) {
  const segments = path.split(" ");
  return childrenOf(commands, path).length > 0
    ? join(...segments, "index.mdx")
    : `${join(...segments)}.mdx`;
}

function proseFiles(dir, base = dir) {
  if (!existsSync(dir)) return [];
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return proseFiles(path, base);
    return entry.name.endsWith(".mdx") ? [relative(base, path)] : [];
  });
}

function proseFor(path) {
  return path === "" ? "index.mdx" : `${path.split(" ").join(sep)}.mdx`;
}

function includePath(page, prose) {
  return relative(dirname(page), prose).split(sep).join("/");
}

function navigation(previous, entries) {
  const kept = (previous ?? []).filter(
    (entry) => entry.startsWith("---") || entries.includes(entry),
  );
  return [...kept, ...entries.filter((entry) => !kept.includes(entry))];
}

function writeFile(path, content) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content);
}

export function writeReference({ catalog, outputs, docsDir, proseDir }) {
  const commands = catalog.commands;
  const known = new Set(["", ...commands.map((command) => command.path)].map(proseFor));
  const orphans = proseFiles(proseDir).filter((file) => !known.has(file));
  if (orphans.length > 0) {
    throw new Error(
      `${orphans.map((file) => join(proseDir, file)).join(", ")} names no visible ocel command; rename it after the command it describes, as a path of its words`,
    );
  }

  const metaPath = join(docsDir, "meta.json");
  const meta = existsSync(metaPath) ? JSON.parse(readFileSync(metaPath, "utf8")) : {};
  rmSync(docsDir, { recursive: true, force: true });

  const proseOf = (path, page) => {
    const prose = join(proseDir, proseFor(path));
    return existsSync(prose) ? includePath(page, prose) : undefined;
  };

  const index = join(docsDir, "index.mdx");
  writeFile(index, renderIndex(commands, { prose: proseOf("", index) }));

  for (const command of commands) {
    const page = join(docsDir, pageFile(commands, command.path));
    const subcommands = childrenOf(commands, command.path);
    writeFile(
      page,
      renderPage(command, {
        output: outputs.get(command.path),
        subcommands,
        prose: proseOf(command.path, page),
      }),
    );
    if (subcommands.length > 0) {
      const folder = {
        title: `ocel ${command.path}`,
        pages: ["index", ...subcommands.map((sub) => sub.path.split(" ").at(-1))],
      };
      writeFile(join(dirname(page), "meta.json"), `${JSON.stringify(folder, null, 2)}\n`);
    }
  }

  const pages = navigation(meta.pages, ["index", ...topLevel(commands).map((c) => c.path)]);
  writeFile(metaPath, `${JSON.stringify({ ...meta, pages }, null, 2)}\n`);
}

function runOcel(binary, args) {
  const env = Object.fromEntries(
    Object.entries(process.env).filter(([name]) => !name.startsWith("OCEL_")),
  );
  return execFileSync(binary, args, {
    cwd: root,
    env: { ...env, OCEL_TELEMETRY: "0", DO_NOT_TRACK: "1" },
    encoding: "utf8",
  });
}

function readCatalog() {
  const scratch = mkdtempSync(join(tmpdir(), "ocel-cli-reference-"));
  try {
    const binary = join(scratch, "ocel");
    execFileSync("go", ["build", "-o", binary, "./ocel"], {
      cwd: join(root, "cli"),
      stdio: "inherit",
    });
    const catalog = JSON.parse(runOcel(binary, ["help", "--json"]));
    const listed = JSON.parse(runOcel(binary, ["schema", "--json"])).data.commands;
    return { catalog, outputs: new Map(listed.map((entry) => [entry.path, entry.output])) };
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}

if (import.meta.main) {
  writeReference({ ...readCatalog(), docsDir: DOCS_DIR, proseDir: PROSE_DIR });
  execFileSync("pnpm", ["exec", "biome", "format", "--write", DOCS_DIR], {
    cwd: root,
    stdio: "inherit",
  });
}
