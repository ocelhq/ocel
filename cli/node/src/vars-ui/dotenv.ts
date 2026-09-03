export interface DotenvEntry {
  key: string;
  value: string;
}

const assignment = /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=(.*)$/;

const escapes: Record<string, string> = {
  n: "\n",
  r: "\r",
  t: "\t",
  '"': '"',
  "\\": "\\",
};

function closingDoubleQuote(body: string): number {
  for (let i = 0; i < body.length; i++) {
    if (body[i] === "\\") {
      i++;
    } else if (body[i] === '"') {
      return i;
    }
  }
  return -1;
}

function unescape(body: string): string {
  return body.replace(/\\(.)/g, (whole, char: string) => escapes[char] ?? whole);
}

function unquoted(raw: string): string {
  const comment = /\s#/.exec(raw);
  return (comment ? raw.slice(0, comment.index) : raw).trim();
}

export function parseDotenv(text: string): DotenvEntry[] {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  const out = new Map<string, string>();
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!.trim();
    if (line === "" || line.startsWith("#")) continue;
    const match = assignment.exec(line);
    if (!match) continue;
    const key = match[1]!;
    const raw = match[2]!;
    const rest = raw.trimStart();
    const quote = rest[0];
    if (quote !== '"' && quote !== "'") {
      out.set(key, unquoted(raw));
      continue;
    }
    let body = rest.slice(1);
    let value = "";
    for (;;) {
      const end = quote === '"' ? closingDoubleQuote(body) : body.indexOf("'");
      if (end >= 0) {
        value += body.slice(0, end);
        break;
      }
      value += `${body}\n`;
      i++;
      if (i >= lines.length) {
        value = value.slice(0, -1);
        break;
      }
      body = lines[i]!;
    }
    out.set(key, quote === '"' ? unescape(value) : value);
  }
  return [...out].map(([key, value]) => ({ key, value }));
}
