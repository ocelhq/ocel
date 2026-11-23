var EMPTY_BODY_HEADER = 'x-ocel-empty-body';
var REMAPPED_PREFIX = 'x-amzn-remapped-';
var RESTORABLE = ['www-authenticate', 'server', 'date', 'content-md5', 'max-forwards'];
var ORIGIN_VARY_HEADER = 'x-ocel-vary';
var CACHE_HEADER = 'x-ocel-cache';
var SERVER_TIMING_HEADER = 'server-timing';
var EDGE_METRIC_PREFIX = 'cdn-';
var EDGE_TIERS = { 'cdn-cache-hit': 'HIT', 'cdn-cache-refresh': 'HIT', 'cdn-cache-miss': 'MISS' };
var DRAFT_COOKIE = '__prerender_bypass';
var BROWSER_REVALIDATES = 'public, max-age=0, must-revalidate';
var UNSHAREABLE = ['private', 'no-store', 'no-cache'];

function restoreRemappedHeaders(headers) {
  var names = Object.keys(headers);
  for (let i = 0; i < names.length; i++) {
    const name = names[i].toLowerCase();
    if (name.indexOf(REMAPPED_PREFIX) !== 0) continue;
    const original = name.slice(REMAPPED_PREFIX.length);
    if (RESTORABLE.indexOf(original) >= 0 && headers[original] === undefined) {
      headers[original] = headers[names[i]];
    }
    delete headers[names[i]];
  }
}

function restoreOriginVary(headers) {
  if (headers[ORIGIN_VARY_HEADER] === undefined) return;
  headers.vary = headers[ORIGIN_VARY_HEADER];
  delete headers[ORIGIN_VARY_HEADER];
}

function directiveNames(cacheControl) {
  var names = [];
  var parts = cacheControl.toLowerCase().split(',');
  for (let i = 0; i < parts.length; i++) names.push(parts[i].split('=')[0].trim());
  return names;
}

function revalidateInBrowser(headers) {
  if (headers['cache-control'] === undefined) return;
  var names = directiveNames(headers['cache-control'].value);
  if (names.indexOf('s-maxage') < 0) return;
  for (let i = 0; i < UNSHAREABLE.length; i++) {
    if (names.indexOf(UNSHAREABLE[i]) >= 0) return;
  }
  headers['cache-control'] = { value: BROWSER_REVALIDATES };
}

function takeEdgeTier(headers) {
  if (headers[SERVER_TIMING_HEADER] === undefined) return null;
  var tier = null;
  var kept = [];
  var metrics = headers[SERVER_TIMING_HEADER].value.split(',');
  for (let i = 0; i < metrics.length; i++) {
    const metric = metrics[i].trim();
    const name = metric.split(';')[0];
    if (name.indexOf(EDGE_METRIC_PREFIX) !== 0) {
      if (metric !== '') kept.push(metric);
    } else if (EDGE_TIERS[name] !== undefined) {
      tier = EDGE_TIERS[name];
    }
  }
  if (kept.length > 0) headers[SERVER_TIMING_HEADER] = { value: kept.join(',') };
  else delete headers[SERVER_TIMING_HEADER];
  return tier;
}

function stampTier(request, headers) {
  var tier = takeEdgeTier(headers);
  if (request.cookies && request.cookies[DRAFT_COOKIE] !== undefined) tier = 'BYPASS';
  else if (tier === 'MISS' && headers[CACHE_HEADER] !== undefined) tier = null;
  if (tier !== null) headers[CACHE_HEADER] = { value: tier };
}

// biome-ignore lint/correctness/noUnusedVariables: CloudFront Functions calls handler by name
function handler(event) {
  var response = event.response;
  restoreRemappedHeaders(response.headers);
  restoreOriginVary(response.headers);
  revalidateInBrowser(response.headers);
  stampTier(event.request, response.headers);
  if (response.headers[EMPTY_BODY_HEADER] === undefined) return response;
  delete response.headers[EMPTY_BODY_HEADER];
  response.body = { encoding: 'text', data: '' };
  return response;
}
