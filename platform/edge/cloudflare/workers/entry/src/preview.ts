export function normalizeBaseDomain(baseDomain: string | undefined): string {
  return (baseDomain ?? "").toLowerCase().replace(/^\.+/, "").replace(/\.+$/, "");
}

export interface PreviewSite {
  baseDomain: string | undefined;
  key: string | undefined;
  slug?: string;
}

export interface PreviewTarget {
  slug: string;
  label: string;
}

const TOKEN_LEN = 16;
const MAC_LEN = 8;
const TAIL_LEN = TOKEN_LEN + MAC_LEN;
const SHORTEST_LABEL_LEN = "p-".length + TAIL_LEN;
const BASE32 = "abcdefghijklmnopqrstuvwxyz234567";

export async function findPreviewTarget(
  host: string,
  site: PreviewSite,
): Promise<PreviewTarget | null> {
  const label = parsePreviewLabel(host, site.baseDomain);
  if (label === null || !site.key) return null;
  if (!(await isSigned(label, site.key))) return null;
  if (site.slug !== undefined) return site.slug ? { slug: site.slug, label } : null;
  return { slug: parseLabelPrefix(label), label };
}

function parseLabelPrefix(label: string): string {
  return label.slice(0, -("-".length + TAIL_LEN));
}

function parsePreviewLabel(host: string, baseDomain: string | undefined): string | null {
  const h = host.toLowerCase().split(":", 1)[0];
  const base = normalizeBaseDomain(baseDomain);
  if (base === "") return null;

  const baseSuffix = `.${base}`;
  if (!h.endsWith(baseSuffix)) return null;

  const label = h.slice(0, -baseSuffix.length);
  if (label === "" || label.includes(".")) return null;
  return label;
}

let imported: { secret: string; key: Promise<CryptoKey> } | undefined;

function importHmacKey(secret: string): Promise<CryptoKey> {
  if (imported?.secret !== secret) {
    imported = {
      secret,
      key: crypto.subtle.importKey(
        "raw",
        new TextEncoder().encode(secret),
        { name: "HMAC", hash: "SHA-256" },
        false,
        ["sign"],
      ),
    };
  }
  return imported.key;
}

async function isSigned(label: string, secret: string): Promise<boolean> {
  if (label.length < SHORTEST_LABEL_LEN) return false;
  const message = label.slice(0, -MAC_LEN);
  const sum = await crypto.subtle.sign(
    "HMAC",
    await importHmacKey(secret),
    new TextEncoder().encode(message),
  );
  const want = encodeBase32(new Uint8Array(sum, 0, (MAC_LEN * 5) / 8));
  const got = label.slice(-MAC_LEN);
  let diff = 0;
  for (let i = 0; i < MAC_LEN; i++) diff |= want.charCodeAt(i) ^ got.charCodeAt(i);
  return diff === 0;
}

function encodeBase32(bytes: Uint8Array): string {
  let out = "";
  let bits = 0;
  let value = 0;
  for (const byte of bytes) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += BASE32[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  return out;
}
