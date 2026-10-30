var EMPTY_BODY_HEADER = 'x-ocel-empty-body';
var REMAPPED_PREFIX = 'x-amzn-remapped-';
var RESTORABLE = ['www-authenticate', 'server', 'date', 'content-md5', 'max-forwards'];
var ORIGIN_VARY_HEADER = 'x-ocel-vary';
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

// biome-ignore lint/correctness/noUnusedVariables: CloudFront Functions calls handler by name
function handler(event) {
  var response = event.response;
  restoreRemappedHeaders(response.headers);
  restoreOriginVary(response.headers);
  revalidateInBrowser(response.headers);
  if (response.headers[EMPTY_BODY_HEADER] === undefined) return response;
  delete response.headers[EMPTY_BODY_HEADER];
  response.body = { encoding: 'text', data: '' };
  return response;
}
