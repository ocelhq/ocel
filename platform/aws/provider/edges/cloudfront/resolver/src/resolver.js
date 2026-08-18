import cf from 'cloudfront';

var kvs = cf.kvs();

var STATIC_PREFIX = '/_next/static/';
var DRAFT_COOKIE = '__prerender_bypass';
var EDGE_HEADER = 'x-ocel-edge';
var EDGE_KIND = 'native';
var CACHE_KEY_HEADER = 'x-ocel-cache-key';
var FORWARDED_HOST_HEADER = 'x-forwarded-host';
var ORIGIN_SECRET_HEADER = 'x-ocel-origin-secret';
var CONTROL_PREFIXES = ['x-ocel-', 'x-middleware-'];
var CONTROL_HEADERS = ['next-resume'];

function headerValue(headers, name) {
  var entry = headers[name];
  if (entry === undefined || entry === null) return null;
  return entry.value === undefined ? null : entry.value;
}

function stripClientControl(headers) {
  var names = Object.keys(headers);
  for (var i = 0; i < names.length; i++) {
    var name = names[i].toLowerCase();
    var control = CONTROL_HEADERS.indexOf(name) >= 0;
    for (var p = 0; !control && p < CONTROL_PREFIXES.length; p++) {
      control = name.indexOf(CONTROL_PREFIXES[p]) === 0;
    }
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

function cacheKey(release, pathname, headers, cookies) {
  var key = release + variantPath(pathname, headers);
  if (cookies && cookies[DRAFT_COOKIE] !== undefined) key += '.draft';
  return key;
}

function assetOriginPath(prefix) {
  var path = prefix === undefined || prefix === null ? '' : String(prefix);
  while (path.length > 0 && path.charAt(0) === '/') path = path.slice(1);
  while (path.length > 0 && path.charAt(path.length - 1) === '/') path = path.slice(0, -1);
  return path === '' ? '' : '/' + path;
}

function refusal(statusCode, statusDescription, body) {
  var headers = {};
  headers['content-type'] = { value: 'text/plain' };
  headers[EDGE_HEADER] = { value: EDGE_KIND };
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

async function routeFor(host) {
  try {
    return { route: await kvs.get(host, { format: 'json' }) };
  } catch (err) {
    var claimed;
    try {
      claimed = await kvs.exists(host);
    } catch (probe) {
      return { unreadable: true };
    }
    return claimed ? { unreadable: true } : { route: null };
  }
}

async function handler(event) {
  var request = event.request;
  var host = headerValue(request.headers, 'host');
  if (host === null) return unknownHost();
  host = host.toLowerCase();

  var found = await routeFor(host);
  if (found.unreadable) return unreadableRoutes();
  var route = found.route;
  if (!route || !route.origin || !route.release) return unknownHost();

  stripClientControl(request.headers);
  request.headers[FORWARDED_HOST_HEADER] = { value: host };
  request.headers[CACHE_KEY_HEADER] = {
    value: cacheKey(route.release, request.uri, request.headers, request.cookies),
  };

  if (request.uri.indexOf(STATIC_PREFIX) === 0) {
    var assets = {
      domainName: route.assets,
      originAccessControlConfig: {
        enabled: true,
        signingBehavior: 'always',
        signingProtocol: 'sigv4',
        originType: 's3',
      },
      customHeaders: {},
    };
    var path = assetOriginPath(route.assetPrefix);
    if (path !== '') assets.originPath = path;
    cf.updateRequestOrigin(assets);
    return request;
  }

  var originHeaders = {};
  originHeaders[ORIGIN_SECRET_HEADER] = route.secret;
  cf.updateRequestOrigin({
    domainName: route.origin,
    originAccessControlConfig: { enabled: false },
    customOriginConfig: { port: 443, protocol: 'https', sslProtocols: ['TLSv1.2'] },
    customHeaders: originHeaders,
  });
  return request;
}
