export function withClientAddress(request: Request): Request {
  const client = request.headers.get("cf-connecting-ip");
  if (!client) return request;
  const headers = new Headers(request.headers);
  const prior = headers.get("x-forwarded-for");
  headers.set("x-forwarded-for", prior ? `${prior}, ${client}` : client);
  return new Request(request, { headers });
}
