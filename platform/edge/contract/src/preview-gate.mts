export const PREVIEW_COOKIE_NAME = "__Host-ocel-preview";
export const PREVIEW_LOGIN_PATH = "/.ocel/preview/login";
export const PREVIEW_BYPASS_HEADER = "x-ocel-preview-bypass";
export const PREVIEW_SET_BYPASS_COOKIE_HEADER = "x-ocel-set-bypass-cookie";
export const PREVIEW_SESSION_SECONDS = 7 * 24 * 60 * 60;

export const PREVIEW_GATE_CACHE_CONTROL = "private, no-store";
export const PREVIEW_ROBOTS_TAG = "noindex";
const LOGIN_REALM = 'Basic realm="Ocel preview", charset="UTF-8"';
const LOGIN_SCRIPT =
  'var p=document.getElementById("password");async function go(){var r=await fetch("' +
  PREVIEW_LOGIN_PATH +
  '",{method:"POST",headers:{Authorization:"Basic "+btoa(String.fromCharCode(...new TextEncoder().encode(":"+p.value)))}});r.ok?location.assign(document.querySelector("main").dataset.next):document.getElementById("incorrect").hidden=!1}';
const SIGNATURE_HEX_LENGTH = 64;
const COOKIE_PURPOSE = "cookie";
const PASSWORD_PURPOSE = "password";
const BYPASS_PURPOSE = "bypass";
const strictUtf8 = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true });

export interface PreviewGate {
  key: string;
  passwordMac: string;
  bypassSecrets: string[];
  allowOptions: string[];
}

export function parsePreviewGate(value: unknown): PreviewGate | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const { key, passwordMac, bypassSecrets, allowOptions } = value as Record<string, unknown>;
  if (typeof key !== "string" || key === "" || typeof passwordMac !== "string") return undefined;
  const bypass = bypassSecrets ?? [];
  const options = allowOptions ?? [];
  if (!isStrings(bypass) || !isStrings(options)) return undefined;
  return { key, passwordMac, bypassSecrets: bypass, allowOptions: options };
}

function isStrings(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}

export interface PreviewRequest {
  method: string;
  host: string;
  target: string;
  headers: Headers;
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

  if (path === PREVIEW_LOGIN_PATH && ["GET", "HEAD", "POST"].includes(request.method)) {
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
    return answerLoginPage(sanitizeNext(request.target), false);
  }

  const headers = new Headers(request.headers);
  headers.delete(PREVIEW_BYPASS_HEADER);
  headers.delete(PREVIEW_SET_BYPASS_COOKIE_HEADER);
  if (hasPassword) headers.delete("authorization");
  dropSessionCookie(headers);
  return {
    kind: "forward",
    headers,
    responseHeaders: new Headers({ "x-robots-tag": PREVIEW_ROBOTS_TAG }),
  };
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
  if (await isBasicPassword(gate, sign, request.headers.get("authorization"))) {
    if (request.method === "POST") return sessionStarted(sign, request.host, nowSeconds);
    return redirect(sign, "/", request.host, nowSeconds);
  }
  if (request.method === "POST") return answerStatus(401);
  return answerLoginPage("/", true);
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
  return new Headers({
    "cache-control": PREVIEW_GATE_CACHE_CONTROL,
    "x-robots-tag": PREVIEW_ROBOTS_TAG,
  });
}

function answerStatus(status: number): PreviewVerdict {
  return { kind: "respond", response: { status, headers: createGateResponseHeaders(), body: "" } };
}

function answerLoginPage(next: string, prompt: boolean): PreviewVerdict {
  const headers = createGateResponseHeaders();
  headers.set("content-type", "text/html; charset=utf-8");
  if (prompt) headers.set("www-authenticate", LOGIN_REALM);
  const body =
    '<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Preview protected</title>' +
    `<main data-next="${escapeHtml(next)}"><p id="incorrect" role="alert" hidden>Incorrect password.</p>` +
    `<label>Password <input id="password" type="password" autocomplete="current-password" autofocus onkeydown="event.key==='Enter'&&go()"></label>` +
    '<button onclick="go()">Continue</button>' +
    `<noscript><p><a href="${PREVIEW_LOGIN_PATH}">Sign in with the browser prompt</a></p></noscript></main>` +
    `<script>${LOGIN_SCRIPT}</script>`;
  return { kind: "respond", response: { status: 401, headers, body } };
}

function sessionCookie(signature: string, expiry: string): string {
  return `${PREVIEW_COOKIE_NAME}=${expiry}.${signature}; Max-Age=${PREVIEW_SESSION_SECONDS}; Path=/; Secure; HttpOnly; SameSite=Lax`;
}

async function sessionStarted(
  sign: Sign,
  host: string,
  nowSeconds: number,
): Promise<PreviewVerdict> {
  const expiry = String(nowSeconds + PREVIEW_SESSION_SECONDS);
  const headers = createGateResponseHeaders();
  headers.set("set-cookie", sessionCookie(await signSession(sign, host, expiry), expiry));
  return { kind: "respond", response: { status: 204, headers, body: "" } };
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
  headers.set("set-cookie", sessionCookie(signature, expiry));
  return { kind: "respond", response: { status: 303, headers, body: "" } };
}
