import { carryClientAddress } from "@platform/edge-contract/client-address";

export function withClientAddress(request: Request): Request {
  const headers = new Headers(request.headers);
  carryClientAddress(headers, request.headers.get("cf-connecting-ip"));
  return new Request(request, { headers });
}
