import cf from 'cloudfront';

var kvs = cf.kvs();

var DRAFT_COOKIE = '__prerender_bypass';
var REVALIDATE_HEADER = 'x-prerender-revalidate';
var ROUTER_HEADER = 'x-ocel-router';
var ROUTER_KIND = 'cloudfront';
var CACHE_KEY_HEADER = 'x-ocel-cache-key';
var FORWARDED_HOST_HEADER = 'x-forwarded-host';
var ORIGIN_SECRET_HEADER = 'x-ocel-origin-secret';
var CONTAINER_HEADER = 'x-ocel-container';
var CONTAINER_ORIGIN_ID = 'containers';
var CONTROL_PREFIX = 'x-middleware-';
var CLIENT_AUTHORIZATION_HEADER = 'x-ocel-client-authorization';
var CLIENT_ADDRESS_HEADER = 'x-ocel-client-address';
var CONTROL_HEADERS = [
  'x-ocel-entry',
  'next-resume',
  ORIGIN_SECRET_HEADER,
  CONTAINER_HEADER,
  CLIENT_AUTHORIZATION_HEADER,
  CLIENT_ADDRESS_HEADER,
];
var ORIGIN_READ_TIMEOUT_SECONDS = 60;
var SHIELDED_METHODS = ['GET', 'HEAD'];

function isUnderAnyPrefix(uri, prefixes) {
  if (!prefixes) return false;
  for (let i = 0; i < prefixes.length; i++) {
    if (uri.indexOf(prefixes[i]) === 0) return true;
  }
  return false;
}

function headerValue(headers, name) {
  var entry = headers[name];
  if (entry === undefined || entry === null) return null;
  return entry.value === undefined ? null : entry.value;
}

function stripClientControl(headers) {
  var names = Object.keys(headers);
  for (let i = 0; i < names.length; i++) {
    const name = names[i].toLowerCase();
    const control = CONTROL_HEADERS.indexOf(name) >= 0 || name.indexOf(CONTROL_PREFIX) === 0;
    if (control) delete headers[names[i]];
  }
}

function variantPath(pathname, headers) {
  if (headerValue(headers, 'rsc') === null) return pathname;

  var nextUrl = headerValue(headers, 'next-url');
  var base = nextUrl !== null ? pathname + '.iu/' + encodeURIComponent(nextUrl) : pathname;

  var segment = headerValue(headers, 'next-router-segment-prefetch');
  if (segment !== null) {
    return base + '.segments/' + encodeURIComponent(segment) + '.segment.rsc';
  }

  var prefetch = headerValue(headers, 'next-router-prefetch');
  if (prefetch === '1') return base + '.prefetch.rsc';
  if (prefetch !== null) return base + '.nostore/' + encodeURIComponent(prefetch);

  return base + '.rsc';
}

function draftSuffix(cookies) {
  if (!cookies || cookies[DRAFT_COOKIE] === undefined) return '';
  // biome-ignore lint/style/useNodejsImportProtocol: CloudFront Functions names its built-in module crypto
  var digest = require('crypto')
    .createHash('sha256')
    .update(String(cookies[DRAFT_COOKIE].value))
    .digest('hex');
  return '.draft-' + digest.slice(0, 16);
}

function cacheKey(release, pathname, headers, cookies) {
  var key = release + variantPath(pathname, headers) + draftSuffix(cookies);
  if (headerValue(headers, REVALIDATE_HEADER) !== null) {
    key += '.revalidate.' + Date.now().toString(36) + Math.random().toString(36).slice(2);
  }
  return key;
}

function assetOriginPath(prefix) {
  var path = prefix === undefined || prefix === null ? '' : String(prefix);
  while (path.length > 0 && path.charAt(0) === '/') path = path.slice(1);
  while (path.length > 0 && path.charAt(path.length - 1) === '/') path = path.slice(0, -1);
  return path === '' ? '' : '/' + path;
}

function originShieldFor(route, method) {
  if (route.shieldRegion && SHIELDED_METHODS.indexOf(method) >= 0) {
    return { enabled: true, region: route.shieldRegion };
  }
  return { enabled: false };
}

function refusal(statusCode, statusDescription, body) {
  var headers = {};
  headers['content-type'] = { value: 'text/plain' };
  headers[ROUTER_HEADER] = { value: ROUTER_KIND };
  return {
    statusCode: statusCode,
    statusDescription: statusDescription,
    headers: headers,
    body: body,
  };
}

function unknownHost() {
  return refusal(404, 'Not Found', 'no deployment answers on this hostname');
}

function unreadableRoutes() {
  return refusal(
    503,
    'Service Unavailable',
    'the edge could not read which deployment answers on this hostname; retry in a moment',
  );
}

async function readKey(key) {
  try {
    return { value: await kvs.get(key, { format: 'json' }) };
  } catch (_err) {
    let claimed;
    try {
      claimed = await kvs.exists(key);
    } catch (_probe) {
      return { unreadable: true };
    }
    return claimed ? { unreadable: true } : { value: null };
  }
}

// biome-ignore lint/correctness/noUnusedVariables: CloudFront Functions calls handler by name
async function handler(event) {
  var request = event.request;
  var host = headerValue(request.headers, 'host');
  if (host === null) return unknownHost();
  host = host.toLowerCase();

  var found = await readKey(host);
  if (found.unreadable) return unreadableRoutes();
  var route = found.value;
  if (!route || !route.origin || !route.release) return unknownHost();

  stripClientControl(request.headers);
  var refused = await gatePreview(request, route, host);
  if (refused) return refused;
  request.headers[FORWARDED_HOST_HEADER] = { value: host };
  request.headers[CACHE_KEY_HEADER] = {
    value: cacheKey(route.release, request.uri, request.headers, request.cookies),
  };

  if (route.assets && isUnderAnyPrefix(request.uri, route.immutable)) {
    const assets = {
      domainName: route.assets,
      originAccessControlConfig: {
        enabled: true,
        signingBehavior: 'always',
        signingProtocol: 'sigv4',
        originType: 's3',
      },
      customHeaders: {},
    };
    const path = assetOriginPath(route.assetPrefix);
    if (path !== '') assets.originPath = path;
    cf.updateRequestOrigin(assets);
    return request;
  }

  if (route.container) {
    request.headers[ORIGIN_SECRET_HEADER] = { value: route.secret };
    request.headers[CONTAINER_HEADER] = { value: route.container };
    cf.selectRequestOriginById(CONTAINER_ORIGIN_ID);
    return request;
  }

  var originHeaders = {};
  originHeaders[ORIGIN_SECRET_HEADER] = route.secret;
  cf.updateRequestOrigin({
    domainName: route.origin,
    originAccessControlConfig: { enabled: false },
    customOriginConfig: { port: 443, protocol: 'https', sslProtocols: ['TLSv1.2'] },
    customHeaders: originHeaders,
    timeouts: { readTimeout: ORIGIN_READ_TIMEOUT_SECONDS },
    originShield: originShieldFor(route, request.method),
  });
  return request;
}
