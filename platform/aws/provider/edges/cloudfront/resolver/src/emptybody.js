var EMPTY_BODY_HEADER = 'x-ocel-empty-body';
var REMAPPED_PREFIX = 'x-amzn-remapped-';
var RESTORABLE = ['www-authenticate', 'server', 'date', 'content-md5', 'max-forwards'];

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

// biome-ignore lint/correctness/noUnusedVariables: CloudFront Functions calls handler by name
function handler(event) {
  var response = event.response;
  restoreRemappedHeaders(response.headers);
  if (response.headers[EMPTY_BODY_HEADER] === undefined) return response;
  delete response.headers[EMPTY_BODY_HEADER];
  response.body = { encoding: 'text', data: '' };
  return response;
}
