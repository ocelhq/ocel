export const PREVIEW_COOKIE_NAME = "__Host-ocel-preview";
export const PREVIEW_LOGIN_PATH = "/.ocel/preview/login";
export const PREVIEW_BYPASS_HEADER = "x-ocel-preview-bypass";
export const PREVIEW_SET_BYPASS_COOKIE_HEADER = "x-ocel-set-bypass-cookie";
export const PREVIEW_SESSION_SECONDS = 7 * 24 * 60 * 60;
export const PREVIEW_LOGIN_BODY_MAX_BYTES = 1024;

const REFUSAL_CACHE_CONTROL = "private, no-store";
const ROBOTS_TAG = "noindex";
const FORM_CONTENT_TYPE = "application/x-www-form-urlencoded";
const SIGNATURE_HEX_LENGTH = 64;

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
  return (await signer(key))("password", password);
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
    return redirect(sign, safeNext(request.target), request.host, nowSeconds);
  }

  if (!(hasPassword || hasBypass || hasSession || allowsOptions(gate, request.method, path))) {
    return refuse(401, safeNext(request.target), false);
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
  if (body.length > PREVIEW_LOGIN_BODY_MAX_BYTES) return bare(413);
  const mediaType = (request.headers.get("content-type") ?? "")
    .split(";", 1)[0]
    ?.trim()
    .toLowerCase();
  if (mediaType !== FORM_CONTENT_TYPE) return bare(415);

  const form = new URLSearchParams(new TextDecoder().decode(body));
  const next = safeNext(form.get("next") ?? "");
  if (!(await isPassword(gate, sign, form.get("password") ?? ""))) return refuse(401, next, true);
  return redirect(sign, next, request.host, nowSeconds);
}

async function isPassword(gate: PreviewGate, sign: Sign, password: string): Promise<boolean> {
  return gate.passwordMac !== "" && equalHex(await sign("password", password), gate.passwordMac);
}

async function isBasicPassword(
  gate: PreviewGate,
  sign: Sign,
  authorization: string | null,
): Promise<boolean> {
  if (authorization === null) return false;
  const space = authorization.indexOf(" ");
  if (space < 0 || authorization.slice(0, space).toLowerCase() !== "basic") return false;
  const encoded = authorization.slice(space + 1).trim();
  if (encoded.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/.test(encoded)) return false;
  const decoded = new TextDecoder().decode(Uint8Array.from(atob(encoded), (c) => c.charCodeAt(0)));
  const colon = decoded.indexOf(":");
  return colon >= 0 && (await isPassword(gate, sign, decoded.slice(colon + 1)));
}

async function isBypassSecret(
  gate: PreviewGate,
  sign: Sign,
  value: string | null,
): Promise<boolean> {
  if (!value) return false;
  const want = await sign("bypass", value);
  let matched = false;
  for (const secret of gate.bypassSecrets) {
    if (secret !== "" && equalHex(await sign("bypass", secret), want)) matched = true;
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
    if (equalHex(await sign("cookie", `${request.host.toLowerCase()}\n${expiry}`), signature))
      return true;
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

function readSessionCookies(headers: Headers): string[] {
  const values: string[] = [];
  for (const pair of (headers.get("cookie") ?? "").split(";")) {
    const trimmed = pair.trim();
    const equals = trimmed.indexOf("=");
    if (equals >= 0 && trimmed.slice(0, equals) === PREVIEW_COOKIE_NAME)
      values.push(trimmed.slice(equals + 1));
  }
  return values;
}

function dropSessionCookie(headers: Headers): void {
  const line = headers.get("cookie");
  if (line === null) return;
  const kept = line
    .split(";")
    .map((pair) => pair.trim())
    .filter((pair) => pair !== "" && pair.split("=", 1)[0] !== PREVIEW_COOKIE_NAME);
  if (kept.length === 0) headers.delete("cookie");
  else headers.set("cookie", kept.join("; "));
}

function safeNext(target: string): string {
  if (!target.startsWith("/") || target.startsWith("//") || target.startsWith("/\\")) return "/";
  if (Array.from(target).some((char) => char.charCodeAt(0) < 0x20 || char.charCodeAt(0) === 0x7f))
    return "/";
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

function refusalHeaders(): Headers {
  return new Headers({ "cache-control": REFUSAL_CACHE_CONTROL, "x-robots-tag": ROBOTS_TAG });
}

function bare(status: number): PreviewVerdict {
  return { kind: "respond", response: { status, headers: refusalHeaders(), body: "" } };
}

function refuse(status: number, next: string, incorrect: boolean): PreviewVerdict {
  const headers = refusalHeaders();
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
  const signature = await sign("cookie", `${host.toLowerCase()}\n${expiry}`);
  const headers = refusalHeaders();
  headers.set("location", location);
  headers.set(
    "set-cookie",
    `${PREVIEW_COOKIE_NAME}=${expiry}.${signature}; Max-Age=${PREVIEW_SESSION_SECONDS}; Path=/; Secure; HttpOnly; SameSite=Lax`,
  );
  return { kind: "respond", response: { status: 303, headers, body: "" } };
}
