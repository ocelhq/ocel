const bodyOwners = new WeakMap<ReadableStream, Response>();

export function retainOwner(response: Response): Response {
  if (response.body) bodyOwners.set(response.body, response);
  return response;
}
