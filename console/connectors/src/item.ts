export const REFUSALS = ["offline", "incompatible", "denied", "lost-lease", "failed"] as const;

export type RefusalReason = (typeof REFUSALS)[number];

export interface Refusal {
  reason: RefusalReason;
  message: string;
}

export type Outcome<T> = { done: true; result: T } | { done: false; refusal: Refusal };

export class ValueError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export function refuse(reason: RefusalReason, message: string): Outcome<never> {
  return { done: false, refusal: { reason, message } };
}

export function done<T>(result: T): Outcome<T> {
  return { done: true, result };
}
