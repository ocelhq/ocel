import RedisMock from "ioredis-mock";

type SetCommand = (key: string, value: string, ...rest: unknown[]) => Promise<unknown>;

export class ValkeyMock extends RedisMock {
  constructor(...args: ConstructorParameters<typeof RedisMock>) {
    super(...args);
    // HACK: ioredis-mock ignores SET's KEEPTTL and drops the TTL, which Valkey keeps.
    const self = this as unknown as { set: SetCommand; pttl(key: string): Promise<number> };
    const set = self.set.bind(this);
    self.set = async (key, value, ...rest) => {
      if (!rest.includes("KEEPTTL")) return set(key, value, ...rest);
      const remaining = await self.pttl(key);
      const options = rest.filter((option) => option !== "KEEPTTL");
      return remaining > 0
        ? set(key, value, "PX", remaining, ...options)
        : set(key, value, ...options);
    };
  }
}
