export function summarize(samples) {
  const values = (samples ?? []).filter((value) => Number.isFinite(value)).sort((a, b) => a - b);
  const n = values.length;
  if (n === 0) {
    return { n: 0, mean: null, stddev: null, min: null, p50: null, p90: null, p99: null, max: null };
  }
  const mean = values.reduce((total, value) => total + value, 0) / n;
  const variance = n === 1 ? 0 : values.reduce((total, value) => total + (value - mean) ** 2, 0) / (n - 1);
  return {
    n,
    mean,
    stddev: Math.sqrt(variance),
    min: values[0],
    p50: percentile(values, 50),
    p90: percentile(values, 90),
    p99: percentile(values, 99),
    max: values[n - 1],
  };
}

export function percentile(sortedValues, p) {
  const n = sortedValues.length;
  if (n === 0) return null;
  const rank = Math.ceil((p / 100) * n);
  return sortedValues[Math.min(n - 1, Math.max(0, rank - 1))];
}

export const PERCENTILE_METHOD = "nearest-rank on the sorted samples, no interpolation";
