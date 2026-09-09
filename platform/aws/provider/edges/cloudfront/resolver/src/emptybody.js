var EMPTY_BODY_HEADER = 'x-ocel-empty-body';

// biome-ignore lint/correctness/noUnusedVariables: CloudFront Functions calls handler by name
function handler(event) {
  var response = event.response;
  if (response.headers[EMPTY_BODY_HEADER] === undefined) return response;
  delete response.headers[EMPTY_BODY_HEADER];
  response.body = { encoding: 'text', data: '' };
  return response;
}
