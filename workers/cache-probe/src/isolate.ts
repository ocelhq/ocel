let id: string | undefined;

export function isolateId(): string {
  return (id ??= crypto.randomUUID().slice(0, 8));
}
