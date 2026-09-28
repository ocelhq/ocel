import { stripVTControlCharacters } from "node:util";

const REFUSED = "✗";
const PRODUCTION_ABSENT = "– not set up — run `ocel bootstrap production` to set it up";
const PRODUCTION_CURRENT = /^ {2}✓ bootstrapped — schema \d+, current$/m;
const BUILTIN_PROXY_VERDICTS = [
  "✓ port 80 answers from this machine",
  "✓ nothing listens on tcp 2019 inside ocel-proxy",
];

function sectionOf(plain: string, heading: string): string | undefined {
  return plain.split(/\n\s*\n/).find((block) => {
    const [first = ""] = block.split("\n");
    return first === heading || first.startsWith(`${heading}  `);
  });
}

export function unbootstrappedMissed(said: string): string | undefined {
  const plain = stripVTControlCharacters(said);
  const production = sectionOf(plain, "Production") ?? "";
  if (production.split("\n").some((line) => line.trim() === PRODUCTION_ABSENT)) {
    return undefined;
  }
  return `\`ocel doctor\` never said production is not set up on a box with no bootstrap:\n${plain}`;
}

export function bootstrappedMissed(said: string, ownsPorts: boolean): string | undefined {
  const plain = stripVTControlCharacters(said);
  const missed: string[] = [];
  if (!PRODUCTION_CURRENT.test(sectionOf(plain, "Production") ?? "")) {
    missed.push("it never calls production bootstrapped and current");
  }
  const checks = sectionOf(plain, "Host checks");
  if (checks === undefined) {
    missed.push(
      "it printed no host checks section, so there is no output an absence can be read over",
    );
  } else if (ownsPorts) {
    for (const verdict of BUILTIN_PROXY_VERDICTS) {
      if (!checks.includes(verdict)) {
        missed.push(`its host checks never say "${verdict}"`);
      }
    }
  }
  if (plain.includes(REFUSED)) {
    missed.push(`it refused something (${REFUSED}) on a box nothing is deployed to yet`);
  }
  if (sectionOf(plain, "Certificates") !== undefined) {
    missed.push("it printed a certificates section over a box that serves no hostname");
  }
  if (missed.length === 0) {
    return undefined;
  }
  return `\`ocel doctor\` over the box bootstrap just wrote: ${missed.join("; ")}:\n${plain}`;
}
