var PREVIEW_COOKIE = '__Host-ocel-preview';
var LOGIN_PATH = '/.ocel/preview/login';
var BYPASS_HEADER = 'x-ocel-preview-bypass';
var SET_BYPASS_COOKIE_HEADER = 'x-ocel-set-bypass-cookie';
var SESSION_SECONDS = 604800;
var LOGIN_REALM = 'Basic realm="Ocel preview", charset="UTF-8"';
var LOGIN_SCRIPT =
  'var p=document.getElementById("password");async function go(){var r=await fetch("' +
  LOGIN_PATH +
  '",{method:"POST",headers:{Authorization:"Basic "+btoa(String.fromCharCode(...new TextEncoder().encode(":"+p.value)))}});r.ok?location.assign(document.querySelector("main").dataset.next):document.getElementById("incorrect").hidden=!1}';
var GATE_KEY_PREFIX = 'gate/';
var HEX_DIGITS = '0123456789abcdef';

function sign(gate, purpose, input) {
  // biome-ignore lint/style/useNodejsImportProtocol: CloudFront Functions names its built-in module crypto
  return require('crypto')
    .createHmac('sha256', gate.key)
    .update(purpose + '\n' + input)
    .digest('hex');
}

function lowerAscii(text) {
  return text.replace(/[A-Z]/g, (c) => c.toLowerCase());
}

function trimBlanks(text) {
  return text.replace(/^[ \t]+|[ \t]+$/g, '');
}

function sameHex(a, b) {
  if (a.length !== b.length) return false;
  var difference = 0;
  for (let i = 0; i < a.length; i++) difference |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return difference === 0;
}

function decodeUtf8(binary) {
  var escaped = '';
  for (let i = 0; i < binary.length; i++) {
    const byte = binary.charCodeAt(i);
    escaped += '%' + HEX_DIGITS.charAt(byte >> 4) + HEX_DIGITS.charAt(byte & 15);
  }
  try {
    return decodeURIComponent(escaped);
  } catch (_err) {
    return null;
  }
}

function valuesOf(entry) {
  return entry.multiValue ? entry.multiValue.map((item) => item.value) : [entry.value];
}

function joinedHeader(headers, name) {
  return headers[name] ? valuesOf(headers[name]).join(', ') : null;
}

function targetOf(request) {
  var parts = [];
  var names = Object.keys(request.querystring);
  for (let i = 0; i < names.length; i++) {
    const values = valuesOf(request.querystring[names[i]]);
    for (let j = 0; j < values.length; j++) {
      parts.push(names[i] + '=' + values[j]);
    }
  }
  return request.uri + (parts.length > 0 ? '?' + parts.join('&') : '');
}

function nextOf(target) {
  if (target.indexOf('/') !== 0 || target.indexOf('//') === 0 || target.indexOf('/\\') === 0)
    return '/';
  if (/[^\x20-\x7e]/.test(target)) return '/';
  return target.split('?', 1)[0] === LOGIN_PATH ? '/' : target;
}

function signSession(gate, host, expiry) {
  return sign(gate, 'cookie', lowerAscii(host) + '\n' + expiry);
}

function isPassword(gate, password) {
  return gate.passwordMac !== '' && sameHex(sign(gate, 'password', password), gate.passwordMac);
}

function hasBasicPassword(gate, authorization) {
  if (authorization === null) return false;
  var space = authorization.indexOf(' ');
  if (space < 0 || lowerAscii(authorization.slice(0, space)) !== 'basic') return false;
  var encoded = trimBlanks(authorization.slice(space + 1));
  if (encoded.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/.test(encoded)) return false;
  var decoded = decodeUtf8(atob(encoded));
  if (decoded === null) return false;
  var colon = decoded.indexOf(':');
  return colon >= 0 && isPassword(gate, decoded.slice(colon + 1));
}

function hasBypassSecret(gate, value) {
  if (!value || /[\u0080-\uffff]/.test(value)) return false;
  var want = sign(gate, 'bypass', value);
  var matched = false;
  for (let i = 0; i < gate.bypassSecrets.length; i++) {
    const secret = gate.bypassSecrets[i];
    if (secret !== '' && sameHex(sign(gate, 'bypass', secret), want)) matched = true;
  }
  return matched;
}

function hasSession(gate, request, host) {
  var cookie = request.cookies[PREVIEW_COOKIE];
  if (!cookie) return false;
  var values = valuesOf(cookie);
  var now = Math.floor(Date.now() / 1000);
  for (let i = 0; i < values.length; i++) {
    const dot = values[i].indexOf('.');
    if (dot < 0) continue;
    const expiry = values[i].slice(0, dot);
    const signature = values[i].slice(dot + 1);
    if (!/^[0-9]+$/.test(expiry) || !/^[0-9a-f]{64}$/.test(signature)) continue;
    if (!Number.isSafeInteger(Number(expiry)) || Number(expiry) <= now) continue;
    if (sameHex(signSession(gate, host, expiry), signature)) return true;
  }
  return false;
}

function allowsOptions(gate, method, path) {
  if (method !== 'OPTIONS' || /[%\\]/.test(path) || path.indexOf('..') >= 0) return false;
  for (let i = 0; i < gate.allowOptions.length; i++) {
    const entry = gate.allowOptions[i];
    const prefix = entry.endsWith('/') ? entry.slice(0, -1) : entry;
    if (path === prefix || path.indexOf(prefix + '/') === 0) return true;
  }
  return false;
}

function gateAnswer(statusCode, statusDescription, headers, body) {
  headers['cache-control'] = { value: 'private, no-store' };
  headers['x-robots-tag'] = { value: 'noindex' };
  headers[ROUTER_HEADER] = { value: ROUTER_KIND };
  return {
    statusCode: statusCode,
    statusDescription: statusDescription,
    headers: headers,
    body: body,
  };
}

function loginPageAnswer(next, prompt) {
  var escaped = next.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
  escaped = escaped.replaceAll('"', '&quot;').replaceAll("'", '&#39;');
  var body =
    '<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Preview protected</title>' +
    '<main data-next="' +
    escaped +
    '"><p id="incorrect" role="alert" hidden>Incorrect password.</p>' +
    '<label>Password <input id="password" type="password" autocomplete="current-password" autofocus onkeydown="event.key==\'Enter\'&&go()"></label>' +
    '<button onclick="go()">Continue</button>' +
    '<noscript><p><a href="' +
    LOGIN_PATH +
    '">Sign in with the browser prompt</a></p></noscript></main>' +
    '<script>' +
    LOGIN_SCRIPT +
    '</script>';
  var headers = { 'content-type': { value: 'text/html; charset=utf-8' } };
  if (prompt) headers['www-authenticate'] = { value: LOGIN_REALM };
  return gateAnswer(401, 'Unauthorized', headers, body);
}

function sessionCookie(gate, host) {
  var expiry = Math.floor(Date.now() / 1000) + SESSION_SECONDS + '';
  return {
    value: expiry + '.' + signSession(gate, host, expiry),
    attributes: 'Max-Age=' + SESSION_SECONDS + '; Path=/; Secure; HttpOnly; SameSite=Lax',
  };
}

function sessionStarted(gate, host) {
  var answer = gateAnswer(204, 'No Content', {}, '');
  answer.cookies = {};
  answer.cookies[PREVIEW_COOKIE] = sessionCookie(gate, host);
  return answer;
}

function answerLogin(gate, request, host) {
  var password = hasBasicPassword(gate, joinedHeader(request.headers, 'authorization'));
  if (password) {
    return request.method === 'POST'
      ? sessionStarted(gate, host)
      : sessionRedirect(gate, '/', host);
  }
  return request.method === 'POST'
    ? gateAnswer(401, 'Unauthorized', {}, '')
    : loginPageAnswer('/', true);
}

function sessionRedirect(gate, location, host) {
  var answer = gateAnswer(303, 'See Other', { location: { value: location } }, '');
  answer.cookies = {};
  answer.cookies[PREVIEW_COOKIE] = sessionCookie(gate, host);
  return answer;
}

function isStrings(value) {
  return Array.isArray(value) && value.every((item) => typeof item === 'string');
}

function isGate(value) {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false;
  if (typeof value.key !== 'string' || value.key === '' || typeof value.passwordMac !== 'string')
    return false;
  return isStrings(value.bypassSecrets) && isStrings(value.allowOptions);
}

function gateUnreadable() {
  var headers = { 'content-type': { value: 'text/plain' } };
  return gateAnswer(
    503,
    'Service Unavailable',
    headers,
    'the edge could not read the keys that gate this preview; retry in a moment',
  );
}

// biome-ignore lint/correctness/noUnusedVariables: the resolver calls it on every routed request of the preview tier
async function gatePreview(request, route, host) {
  if (!route.stack) return null;
  var read = await readKey(GATE_KEY_PREFIX + route.stack);
  if (read.unreadable || (read.value && !isGate(read.value))) return gateUnreadable();
  var gate = read.value;
  if (!gate) return null;

  var path = request.uri;
  if (
    path === LOGIN_PATH &&
    (request.method === 'POST' || request.method === 'GET' || request.method === 'HEAD')
  ) {
    return answerLogin(gate, request, host);
  }

  var hasPassword = hasBasicPassword(gate, joinedHeader(request.headers, 'authorization'));
  var hasBypass = hasBypassSecret(gate, joinedHeader(request.headers, BYPASS_HEADER));
  var admitted = hasPassword || hasBypass || hasSession(gate, request, host);

  var readsOrHeads = request.method === 'GET' || request.method === 'HEAD';
  if (
    hasBypass &&
    joinedHeader(request.headers, SET_BYPASS_COOKIE_HEADER) === 'true' &&
    readsOrHeads
  ) {
    return sessionRedirect(gate, nextOf(targetOf(request)), host);
  }
  if (!(admitted || allowsOptions(gate, request.method, path))) {
    return loginPageAnswer(nextOf(targetOf(request)), false);
  }

  delete request.headers[BYPASS_HEADER];
  delete request.headers[SET_BYPASS_COOKIE_HEADER];
  if (hasPassword) delete request.headers.authorization;
  delete request.cookies[PREVIEW_COOKIE];
  return null;
}
