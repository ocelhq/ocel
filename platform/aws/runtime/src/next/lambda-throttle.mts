export function isLambdaThrottle(response: Response): boolean {
  return response.status === 429 && response.headers.has("x-amzn-errortype");
}
