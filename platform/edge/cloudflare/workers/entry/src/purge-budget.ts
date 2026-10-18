export interface PurgeBudget {
  purgesPerMinute: number;
  tagsPerPurge: number;
}

function isPositiveWholeNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

function parseDeclared(declared: string): Partial<PurgeBudget> | undefined {
  try {
    return JSON.parse(declared);
  } catch {
    return undefined;
  }
}

export function readPurgeBudget(declared: string | undefined): PurgeBudget {
  const budget = declared ? parseDeclared(declared) : undefined;
  if (
    !isPositiveWholeNumber(budget?.purgesPerMinute) ||
    !isPositiveWholeNumber(budget.tagsPerPurge)
  ) {
    throw new Error(
      `ocel: OCEL_PURGE_BUDGET is "${declared ?? ""}", and the purge budget the deploy writes is a JSON object of positive whole purgesPerMinute and tagsPerPurge`,
    );
  }
  return { purgesPerMinute: budget.purgesPerMinute, tagsPerPurge: budget.tagsPerPurge };
}
