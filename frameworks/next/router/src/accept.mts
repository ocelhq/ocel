const VALID_MEDIA =
  /^(?:\*\/\*)|(?:[\w!#$%&'*+\-.^`|~]+\/\*)|(?:[\w!#$%&'*+\-.^`|~]+\/[\w!#$%&'*+\-.^`|~]+)$/;

interface MediaSelection {
  token: string;
  type: string;
  subtype: string;
  params: Record<string, string>;
  original: string;
  specificity: number;
  q: number;
  pos: number;
}

export function mediaType(header: string, preferences: string[]): string {
  const selections = mediaSelections(header, preferences);
  return selections[0] ?? "";
}

function normalizeAccept(header: string): {
  header: string;
  quoted: Record<string, string>;
} {
  const quoted: Record<string, string> = {};
  let normalized = header || "*/*";
  if (normalized.includes('"')) {
    let index = 0;
    normalized = normalized.replace(/="([^"]*)"/g, (_match, value: string) => {
      const key = `"${++index}`;
      quoted[key] = value;
      return `=${key}`;
    });
  }
  return { header: normalized.replace(/[ \t]/g, ""), quoted };
}

function innerSort(a: MediaSelection, b: MediaSelection, key: "type" | "subtype"): number {
  if (a[key] === "*") return 1;
  if (b[key] === "*") return -1;
  return a[key] < b[key] ? -1 : 1;
}

function sortSelections(a: MediaSelection, b: MediaSelection): number {
  if (b.q !== a.q) return b.q - a.q;
  if (a.type !== b.type) return innerSort(a, b, "type");
  if (a.subtype !== b.subtype) return innerSort(a, b, "subtype");
  if (a.specificity !== b.specificity) return b.specificity - a.specificity;
  return a.pos - b.pos;
}

function mediaSelections(header: string, preferences: string[]): string[] {
  const { header: normalized, quoted } = normalizeAccept(header);
  const parts = normalized.split(",");
  const selections: MediaSelection[] = [];
  const byToken: Record<string, MediaSelection> = Object.create(null);

  for (let pos = 0; pos < parts.length; ++pos) {
    const part = parts[pos];
    if (!part) continue;
    const pieces = part.split(";");
    const token = (pieces.shift() ?? "").toLowerCase();
    if (!VALID_MEDIA.test(token)) continue;

    const params: Record<string, string> = {};
    let q: number | undefined;
    let seenQ = false;
    for (const piece of pieces) {
      const pair = piece.split("=");
      if (pair.length !== 2 || !pair[1]) return [];
      const [key = "", raw = ""] = pair;
      if (key === "q" || key === "Q") {
        seenQ = true;
        const parsed = parseFloat(raw);
        q = !Number.isFinite(parsed) || parsed > 1 || (parsed < 0.001 && parsed !== 0)
          ? 1
          : parsed;
      } else if (!seenQ) {
        params[key] = raw[0] === '"' ? `"${quoted[raw]}"` : raw;
      }
    }

    const names = Object.keys(params);
    const [type = "", subtype = ""] = token.split("/");
    const selection: MediaSelection = {
      token,
      type,
      subtype,
      params,
      original: [""].concat(names.map((name) => `${name}=${params[name]}`)).join(";"),
      specificity: names.length,
      q: q ?? 1,
      pos,
    };
    byToken[token] = selection;
    if (selection.q) selections.push(selection);
  }

  selections.sort(sortSelections);
  return preferred(byToken, selections, preferences);
}

function preferred(
  byToken: Record<string, MediaSelection>,
  selections: MediaSelection[],
  preferences: string[],
): string[] {
  if (!preferences.length) {
    return selections.map((selection) => selection.token + selection.original);
  }

  const bySubtype: Record<string, Record<string, string>> = Object.create(null);
  const byLowered: Record<string, string> = Object.create(null);
  let anyType = false;
  for (const preference of preferences) {
    const lowered = preference.toLowerCase();
    byLowered[lowered] = preference;
    const [type = "", subtype = ""] = lowered.split("/");
    if (type === "*") {
      anyType = true;
      continue;
    }
    const known = bySubtype[type] ?? Object.create(null);
    bySubtype[type] = known;
    known[subtype] = preference;
  }

  const result: string[] = [];
  for (const selection of selections) {
    const { token, type, subtype, original } = selection;
    const subtypes = bySubtype[type];
    if (type === "*") {
      for (const [lowered, preference] of Object.entries(byLowered)) {
        if (!byToken[lowered]) result.push(preference);
      }
      if (anyType) result.push("*/*");
      continue;
    }
    if (anyType) {
      result.push((byLowered[token] || token) + original);
      continue;
    }
    if (subtype !== "*") {
      const exact = byLowered[token];
      if (exact || subtypes?.["*"]) result.push((exact || token) + original);
      continue;
    }
    if (subtypes) {
      for (const [name, preference] of Object.entries(subtypes)) {
        if (!byToken[`${type}/${name}`]) result.push(preference);
      }
    }
  }
  return result;
}
