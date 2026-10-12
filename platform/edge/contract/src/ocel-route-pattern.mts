const maxCodePoint = 0x10ffff;

export const repeatBound = /^\{(\d+)(?:,(\d*))?\}/;

type Range = readonly [number, number];

type CodePoints = readonly Range[];

interface PatternItem {
  set: CodePoints;
  mandatory: boolean;
  unbounded: boolean;
  zeroWidth: boolean;
}

interface Quantifier {
  mandatory: boolean;
  unbounded: boolean;
}

const digitCodePoints: CodePoints = [[0x30, 0x39]];
const wordCodePoints: CodePoints = [
  [0x30, 0x39],
  [0x41, 0x5a],
  [0x5f, 0x5f],
  [0x61, 0x7a],
];
const allCodePoints: CodePoints = [[0, maxCodePoint]];

function mergeCodePoints(ranges: CodePoints): CodePoints {
  const merged: [number, number][] = [];
  for (const [lo, hi] of [...ranges].sort((a, b) => a[0] - b[0])) {
    const last = merged[merged.length - 1];
    if (last && lo <= last[1] + 1) last[1] = Math.max(last[1], hi);
    else merged.push([lo, hi]);
  }
  return merged;
}

function unionCodePoints(a: CodePoints, b: CodePoints): CodePoints {
  return mergeCodePoints([...a, ...b]);
}

function invertCodePoints(set: CodePoints): CodePoints {
  const inverted: [number, number][] = [];
  let next = 0;
  for (const [lo, hi] of mergeCodePoints(set)) {
    if (lo > next) inverted.push([next, lo - 1]);
    next = hi + 1;
  }
  if (next <= maxCodePoint) inverted.push([next, maxCodePoint]);
  return inverted;
}

function intersectCodePoints(a: CodePoints, b: CodePoints): CodePoints {
  const shared: [number, number][] = [];
  for (const [aLo, aHi] of a) {
    for (const [bLo, bHi] of b) {
      const lo = Math.max(aLo, bLo);
      const hi = Math.min(aHi, bHi);
      if (lo <= hi) shared.push([lo, hi]);
    }
  }
  return mergeCodePoints(shared);
}

function escapedCodePoints(c: number): CodePoints {
  switch (String.fromCodePoint(c)) {
    case "d":
      return digitCodePoints;
    case "D":
      return invertCodePoints(digitCodePoints);
    case "w":
      return wordCodePoints;
    case "W":
      return invertCodePoints(wordCodePoints);
    case "n":
      return [[0x0a, 0x0a]];
    case "r":
      return [[0x0d, 0x0d]];
    case "t":
      return [[0x09, 0x09]];
    default:
      return [[c, c]];
  }
}

function isSingleCodePoint(set: CodePoints): boolean {
  return set.length === 1 && set[0]![0] === set[0]![1];
}

class PatternReader {
  private readonly src: number[];
  private pos = 0;

  constructor(src: string) {
    this.src = Array.from(src, (c) => c.codePointAt(0)!);
  }

  private peeks(c: string): boolean {
    return this.pos < this.src.length && this.src[this.pos] === c.codePointAt(0);
  }

  readAlternation(): PatternItem[][] | string {
    const branches: PatternItem[][] = [];
    for (;;) {
      const branch = this.readSequence();
      if (typeof branch === "string") return branch;
      branches.push(branch);
      if (!this.peeks("|")) return branches;
      this.pos++;
    }
  }

  private readSequence(): PatternItem[] | string {
    const items: PatternItem[] = [];
    while (this.pos < this.src.length && !this.peeks("|") && !this.peeks(")")) {
      const atom = this.readAtom();
      if (typeof atom === "string") return atom;
      items.push(...atom);
    }
    return refuseOverlappingItems(items) ?? items;
  }

  private readAtom(): PatternItem[] | string {
    const c = this.src[this.pos++]!;
    switch (String.fromCodePoint(c)) {
      case "(":
        return this.readGroup();
      case "[": {
        const set = this.readClass();
        return typeof set === "string" ? set : [this.readQuantified(set)];
      }
      case ".":
        return [this.readQuantified(allCodePoints)];
      case "^":
      case "$":
        return [zeroWidthItem()];
      case "\\": {
        if (this.pos >= this.src.length) break;
        const escaped = this.src[this.pos++]!;
        if (escaped === 0x62 || escaped === 0x42) return [zeroWidthItem()];
        return [this.readQuantified(escapedCodePoints(escaped))];
      }
    }
    return [this.readQuantified([[c, c]])];
  }

  private readGroup(): PatternItem[] | string {
    if (this.peeks("?") && this.src[this.pos + 1] === 0x3a) this.pos += 2;
    const branches = this.readAlternation();
    if (typeof branches === "string") return branches;
    if (!this.peeks(")")) return "a pattern closes every group";
    this.pos++;
    const quantifier = this.readQuantifier();
    const [only] = branches;
    if (branches.length === 1 && only && !quantifier.unbounded) {
      return quantifier.mandatory ? only : only.map((item) => ({ ...item, mandatory: false }));
    }
    const opaque: PatternItem = {
      set: [],
      mandatory: quantifier.mandatory,
      unbounded: quantifier.unbounded,
      zeroWidth: true,
    };
    for (const branch of branches) {
      let holdsMandatory = false;
      for (const item of branch) {
        opaque.set = unionCodePoints(opaque.set, item.set);
        opaque.unbounded ||= item.unbounded;
        opaque.zeroWidth &&= item.zeroWidth;
        holdsMandatory ||= item.mandatory && !item.zeroWidth;
      }
      opaque.mandatory &&= holdsMandatory;
    }
    return [opaque];
  }

  private readQuantified(set: CodePoints): PatternItem {
    const quantifier = this.readQuantifier();
    return { set, ...quantifier, zeroWidth: false };
  }

  private readQuantifier(): Quantifier {
    let quantifier: Quantifier = { mandatory: true, unbounded: false };
    if (this.pos >= this.src.length) return quantifier;
    const c = String.fromCodePoint(this.src[this.pos]!);
    if (c === "*") {
      quantifier = { mandatory: false, unbounded: true };
      this.pos++;
    } else if (c === "+") {
      quantifier = { mandatory: true, unbounded: true };
      this.pos++;
    } else if (c === "?") {
      quantifier = { mandatory: false, unbounded: false };
      this.pos++;
    } else if (c === "{") {
      const bounds = repeatBound.exec(
        this.src
          .slice(this.pos)
          .map((point) => String.fromCodePoint(point))
          .join(""),
      );
      if (!bounds) return quantifier;
      quantifier = {
        mandatory: Number(bounds[1]) > 0,
        unbounded: bounds[0].includes(",") && (bounds[2] ?? "") === "",
      };
      this.pos += Array.from(bounds[0]).length;
    } else {
      return quantifier;
    }
    if (this.peeks("?")) this.pos++;
    return quantifier;
  }

  private readClass(): CodePoints | string {
    const negated = this.peeks("^");
    if (negated) this.pos++;
    let members: CodePoints = [];
    while (this.pos < this.src.length && !this.peeks("]")) {
      let c = this.src[this.pos++]!;
      let member: CodePoints = [[c, c]];
      if (c === 0x5c && this.pos < this.src.length) {
        member = escapedCodePoints(this.src[this.pos++]!);
        if (!isSingleCodePoint(member)) {
          members = unionCodePoints(members, member);
          continue;
        }
        c = member[0]![0];
      }
      if (this.peeks("-") && this.pos + 1 < this.src.length && this.src[this.pos + 1] !== 0x5d) {
        this.pos++;
        let hi = this.src[this.pos++]!;
        if (hi === 0x5c && this.pos < this.src.length) {
          const escaped = escapedCodePoints(this.src[this.pos++]!);
          if (!isSingleCodePoint(escaped))
            return "a pattern ends no class range with a class escape";
          hi = escaped[0]![0];
        }
        if (hi < c) return "a pattern orders every class range";
        member = [[c, hi]];
      }
      members = unionCodePoints(members, member);
    }
    if (this.pos >= this.src.length) return "a pattern closes every class";
    this.pos++;
    return negated ? invertCodePoints(members) : mergeCodePoints(members);
  }
}

function zeroWidthItem(): PatternItem {
  return { set: [], mandatory: false, unbounded: false, zeroWidth: true };
}

function hasSeparator(between: readonly PatternItem[], shared: CodePoints): boolean {
  return between.some(
    (item) =>
      item.mandatory && !item.zeroWidth && intersectCodePoints(item.set, shared).length === 0,
  );
}

function refuseOverlappingItems(items: readonly PatternItem[]): string | undefined {
  for (const [i, x] of items.entries()) {
    if (!x.unbounded || x.zeroWidth) continue;
    for (let j = i + 1; j < items.length; j++) {
      const w = items[j]!;
      if (!w.unbounded || w.zeroWidth) continue;
      const shared = intersectCodePoints(x.set, w.set);
      if (shared.length > 0 && !hasSeparator(items.slice(i + 1, j), shared)) {
        return "a pattern repeats two parts without bound that can match the same characters, with no required character between them that one of them cannot match";
      }
    }
  }
  return undefined;
}

export function refuseOverlappingRepeats(src: string): string | undefined {
  const branches = new PatternReader(src).readAlternation();
  return typeof branches === "string" ? branches : undefined;
}
