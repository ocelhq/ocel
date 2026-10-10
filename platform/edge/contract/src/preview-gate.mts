export const PREVIEW_COOKIE_NAME = "__Host-ocel-preview";
export const PREVIEW_LOGIN_PATH = "/.ocel/preview/login";
export const PREVIEW_BYPASS_HEADER = "x-ocel-preview-bypass";
export const PREVIEW_SET_BYPASS_COOKIE_HEADER = "x-ocel-set-bypass-cookie";
export const PREVIEW_SESSION_SECONDS = 7 * 24 * 60 * 60;
export const PREVIEW_LOGIN_BODY_MAX_BYTES = 1024;

const GATE_CACHE_CONTROL = "private, no-store";
const ROBOTS_TAG = "noindex";
const FORM_CONTENT_TYPE = "application/x-www-form-urlencoded";
const SIGNATURE_HEX_LENGTH = 64;
const COOKIE_PURPOSE = "cookie";
const PASSWORD_PURPOSE = "password";
const BYPASS_PURPOSE = "bypass";
const AMPERSAND = 0x26;
const EQUALS_SIGN = 0x3d;
const PLUS_SIGN = 0x2b;
const PERCENT_SIGN = 0x25;
const SPACE = 0x20;

const strictUtf8 = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true });

export interface PreviewGate {
  key: string;
  passwordMac: string;
  bypassSecrets: string[];
  allowOptions: string[];
}

export interface PreviewRequest {
  method: string;
  host: string;
  target: string;
  headers: Headers;
  body?: string | Uint8Array;
}

export interface PreviewResponse {
  status: number;
  headers: Headers;
  body: string;
}

export type PreviewVerdict =
  | { kind: "forward"; headers: Headers; responseHeaders: Headers }
  | { kind: "respond"; response: PreviewResponse };

export async function hashPreviewPassword(key: string, password: string): Promise<string> {
  return (await signer(key))(PASSWORD_PURPOSE, password);
}

export async function checkPreview(
  gate: PreviewGate,
  request: PreviewRequest,
  nowSeconds: number,
): Promise<PreviewVerdict> {
  const sign = await signer(gate.key);
  const path = request.target.split("?", 1)[0] ?? "";

  if (request.method === "POST" && path === PREVIEW_LOGIN_PATH) {
    return checkLogin(gate, sign, request, nowSeconds);
  }

  const hasPassword = await isBasicPassword(gate, sign, request.headers.get("authorization"));
  const hasBypass = await isBypassSecret(gate, sign, request.headers.get(PREVIEW_BYPASS_HEADER));
  const hasSession = await isSession(sign, request, nowSeconds);

  if (
    hasBypass &&
    request.headers.get(PREVIEW_SET_BYPASS_COOKIE_HEADER) === "true" &&
    (request.method === "GET" || request.method === "HEAD")
  ) {
    return redirect(sign, sanitizeNext(request.target), request.host, nowSeconds);
  }

  if (!(hasPassword || hasBypass || hasSession || allowsOptions(gate, request.method, path))) {
    return answerLoginForm(401, sanitizeNext(request.target), false);
  }

  const headers = new Headers(request.headers);
  headers.delete(PREVIEW_BYPASS_HEADER);
  headers.delete(PREVIEW_SET_BYPASS_COOKIE_HEADER);
  if (hasPassword) headers.delete("authorization");
  dropSessionCookie(headers);
  return { kind: "forward", headers, responseHeaders: new Headers({ "x-robots-tag": ROBOTS_TAG }) };
}

type Sign = (purpose: string, input: string) => Promise<string>;

async function signer(key: string): Promise<Sign> {
  const encoder = new TextEncoder();
  const imported = await crypto.subtle.importKey(
    "raw",
    encoder.encode(key),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  return async (purpose, input) => {
    const digest = await crypto.subtle.sign(
      "HMAC",
      imported,
      encoder.encode(`${purpose}\n${input}`),
    );
    return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join(
      "",
    );
  };
}

function signSession(sign: Sign, host: string, expiry: string): Promise<string> {
  return sign(COOKIE_PURPOSE, `${toLowerAscii(host)}\n${expiry}`);
}

function equalHex(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let difference = 0;
  for (let i = 0; i < a.length; i++) difference |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return difference === 0;
}

async function checkLogin(
  gate: PreviewGate,
  sign: Sign,
  request: PreviewRequest,
  nowSeconds: number,
): Promise<PreviewVerdict> {
  const body =
    typeof request.body === "string"
      ? new TextEncoder().encode(request.body)
      : (request.body ?? new Uint8Array());
  if (body.length > PREVIEW_LOGIN_BODY_MAX_BYTES) return answerStatus(413);
  const mediaType = (request.headers.get("content-type") ?? "").split(";", 1)[0] ?? "";
  if (toLowerAscii(trimHttpWhitespace(mediaType)) !== FORM_CONTENT_TYPE) return answerStatus(415);

  const form = parseForm(body);
  const next = sanitizeNext(form.get("next") ?? "");
  const password = form.get("password");
  if (!(await isPassword(gate, sign, password === undefined ? "" : password))) {
    return answerLoginForm(401, next, true);
  }
  return redirect(sign, next, request.host, nowSeconds);
}

async function isPassword(
  gate: PreviewGate,
  sign: Sign,
  password: string | null,
): Promise<boolean> {
  return (
    gate.passwordMac !== "" &&
    password !== null &&
    equalHex(await sign(PASSWORD_PURPOSE, password), gate.passwordMac)
  );
}

async function isBasicPassword(
  gate: PreviewGate,
  sign: Sign,
  authorization: string | null,
): Promise<boolean> {
  if (authorization === null) return false;
  const space = authorization.indexOf(" ");
  if (space < 0 || toLowerAscii(authorization.slice(0, space)) !== "basic") return false;
  const encoded = trimHttpWhitespace(authorization.slice(space + 1));
  if (encoded.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/.test(encoded)) return false;
  const decoded = decodeUtf8(Uint8Array.from(atob(encoded), (char) => char.charCodeAt(0)));
  if (decoded === null) return false;
  const colon = decoded.indexOf(":");
  return colon >= 0 && (await isPassword(gate, sign, decoded.slice(colon + 1)));
}

async function isBypassSecret(
  gate: PreviewGate,
  sign: Sign,
  value: string | null,
): Promise<boolean> {
  if (!value || /[\u0080-\uffff]/.test(value)) return false;
  const want = await sign(BYPASS_PURPOSE, value);
  let matched = false;
  for (const secret of gate.bypassSecrets) {
    if (secret !== "" && equalHex(await sign(BYPASS_PURPOSE, secret), want)) matched = true;
  }
  return matched;
}

async function isSession(
  sign: Sign,
  request: PreviewRequest,
  nowSeconds: number,
): Promise<boolean> {
  for (const value of readSessionCookies(request.headers)) {
    const dot = value.indexOf(".");
    if (dot < 0) continue;
    const expiry = value.slice(0, dot);
    const signature = value.slice(dot + 1);
    if (
      !/^[0-9]+$/.test(expiry) ||
      signature.length !== SIGNATURE_HEX_LENGTH ||
      !/^[0-9a-f]+$/.test(signature)
    ) {
      continue;
    }
    if (!Number.isSafeInteger(Number(expiry)) || Number(expiry) <= nowSeconds) continue;
    if (equalHex(await signSession(sign, request.host, expiry), signature)) return true;
  }
  return false;
}

function allowsOptions(gate: PreviewGate, method: string, path: string): boolean {
  if (method !== "OPTIONS" || /[%\\]/.test(path) || path.includes("..")) return false;
  return gate.allowOptions.some((entry) => {
    const prefix = entry.endsWith("/") ? entry.slice(0, -1) : entry;
    return path === prefix || path.startsWith(`${prefix}/`);
  });
}

function readCookiePairs(headers: Headers): string[] {
  return (headers.get("cookie") ?? "")
    .split(";")
    .map(trimHttpWhitespace)
    .filter((pair) => pair !== "");
}

function readSessionCookies(headers: Headers): string[] {
  const values: string[] = [];
  for (const pair of readCookiePairs(headers)) {
    const equals = pair.indexOf("=");
    if (equals >= 0 && pair.slice(0, equals) === PREVIEW_COOKIE_NAME)
      values.push(pair.slice(equals + 1));
  }
  return values;
}

function dropSessionCookie(headers: Headers): void {
  const kept = readCookiePairs(headers).filter(
    (pair) => pair.split("=", 1)[0] !== PREVIEW_COOKIE_NAME,
  );
  if (kept.length === 0) headers.delete("cookie");
  else headers.set("cookie", kept.join("; "));
}

function parseForm(body: Uint8Array): Map<string, string | null> {
  const form = new Map<string, string | null>();
  let start = 0;
  for (let end = 0; end <= body.length; end++) {
    if (end < body.length && body[end] !== AMPERSAND) continue;
    const pair = body.subarray(start, end);
    start = end + 1;
    if (pair.length === 0) continue;
    const equals = pair.indexOf(EQUALS_SIGN);
    const name = decodeFormComponent(equals < 0 ? pair : pair.subarray(0, equals));
    const value = equals < 0 ? "" : decodeFormComponent(pair.subarray(equals + 1));
    if (name !== null && !form.has(name)) form.set(name, value);
  }
  return form;
}

function decodeFormComponent(component: Uint8Array): string | null {
  const decoded: number[] = [];
  for (let i = 0; i < component.length; i++) {
    const byte = component[i]!;
    if (byte === PLUS_SIGN) {
      decoded.push(SPACE);
      continue;
    }
    if (byte === PERCENT_SIGN && i + 2 < component.length) {
      const digits = String.fromCharCode(...component.subarray(i + 1, i + 3));
      if (/^[0-9A-Fa-f]{2}$/.test(digits)) {
        decoded.push(Number.parseInt(digits, 16));
        i += 2;
        continue;
      }
    }
    decoded.push(byte);
  }
  return decodeUtf8(Uint8Array.from(decoded));
}

function decodeUtf8(bytes: Uint8Array): string | null {
  try {
    return strictUtf8.decode(bytes);
  } catch {
    return null;
  }
}

function trimHttpWhitespace(text: string): string {
  return text.replace(/^[ \t]+|[ \t]+$/g, "");
}

function toLowerAscii(text: string): string {
  return text.replace(/[A-Z]/g, (letter) => letter.toLowerCase());
}

function sanitizeNext(target: string): string {
  if (!target.startsWith("/") || target.startsWith("//") || target.startsWith("/\\")) return "/";
  if (/[^\x20-\x7e]/.test(target)) return "/";
  if (target.split("?", 1)[0] === PREVIEW_LOGIN_PATH) return "/";
  return target;
}

function escapeHtml(text: string): string {
  return text
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

function createGateResponseHeaders(): Headers {
  return new Headers({ "cache-control": GATE_CACHE_CONTROL, "x-robots-tag": ROBOTS_TAG });
}

function answerStatus(status: number): PreviewVerdict {
  return { kind: "respond", response: { status, headers: createGateResponseHeaders(), body: "" } };
}

function answerLoginForm(status: number, next: string, incorrect: boolean): PreviewVerdict {
  const headers = createGateResponseHeaders();
  headers.set("content-type", "text/html; charset=utf-8");
  const notice = incorrect ? '<p role="alert">Incorrect password.</p>' : "";
  const body =
    '<!doctype html><html lang="en"><head><meta charset="utf-8">' +
    '<meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex">' +
    `<title>Preview protected</title></head><body>${notice}` +
    `<form method="post" action="${PREVIEW_LOGIN_PATH}">` +
    '<label>Password <input type="password" name="password" autocomplete="current-password" autofocus required></label>' +
    `<input type="hidden" name="next" value="${escapeHtml(next)}">` +
    '<button type="submit">Continue</button></form></body></html>';
  return { kind: "respond", response: { status, headers, body } };
}

async function redirect(
  sign: Sign,
  location: string,
  host: string,
  nowSeconds: number,
): Promise<PreviewVerdict> {
  const expiry = String(nowSeconds + PREVIEW_SESSION_SECONDS);
  const signature = await signSession(sign, host, expiry);
  const headers = createGateResponseHeaders();
  headers.set("location", location);
  headers.set(
    "set-cookie",
    `${PREVIEW_COOKIE_NAME}=${expiry}.${signature}; Max-Age=${PREVIEW_SESSION_SECONDS}; Path=/; Secure; HttpOnly; SameSite=Lax`,
  );
  return { kind: "respond", response: { status: 303, headers, body: "" } };
}
