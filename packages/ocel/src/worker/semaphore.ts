export class Semaphore {
  private available: number;
  private waiting: (() => void)[] = [];

  constructor(permits: number) {
    this.available = permits;
  }

  acquire(signal: AbortSignal): Promise<void> {
    if (this.available > 0) {
      this.available--;
      return Promise.resolve();
    }
    if (signal.aborted) return Promise.reject(signal.reason);
    return new Promise((resolve, reject) => {
      const takePermit = () => {
        signal.removeEventListener("abort", stopWaiting);
        resolve();
      };
      const stopWaiting = () => {
        const index = this.waiting.indexOf(takePermit);
        if (index !== -1) this.waiting.splice(index, 1);
        reject(signal.reason);
      };
      this.waiting.push(takePermit);
      signal.addEventListener("abort", stopWaiting, { once: true });
    });
  }

  release(): void {
    const next = this.waiting.shift();
    if (next) next();
    else this.available++;
  }
}
