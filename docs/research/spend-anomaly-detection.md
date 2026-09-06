# Anomaly detection for sparse, bursty, cheap spend

Retrieved 2026-09-06. Every number below carries that date unless stated otherwise.

## Headlines

1. Every commercial cost-anomaly product has a **dollar floor** that structurally excludes ocel's target case: Vantage suppresses anything under **$5/day** and under **0.5% of the report's daily total**; Kubecost ships a "Minimum Cost" knob for the same reason; CloudZero's automatic threshold is a sliding scale off trailing-30-day spend; AWS alerts on absolute dollar impact or impact percentage. The Aug-9 fluke day ($1.74) is below all of them.
2. AWS Cost Anomaly Detection needs **10 days of historical service usage** before it detects for a new service, runs **~3×/day** on Cost Explorer data with **up to 24h delay**, and a new monitor takes 24h to start. Azure trains on **60 days** (WaveNet, univariate, reconstruction-based) and runs **36h after end of day**. Vantage requires **>12 days** per series. None of them help in week one.
3. AWS's **Impact % is "N/A" when expected spend is zero** — documented. The $0.00 → $1.74 transition, the exact ocel case, is the one AWS cannot express as a percentage.
4. **The magnitude problem is solved by projecting the run rate, not by lowering the dollar floor.** $1.74/day against a $0.02 baseline is **+$52/month if it sticks**. Threshold on projected monthly impact (default $10/mo) and the fluke day clears the gate while a $0.05 deploy blip ($1.50/mo) does not.
5. **Recommended daily detector: trailing-28-day median + MAD (×1.4826, Leys 2013) with a dispersion floor, gated by projected monthly impact.** Fire when `z ≥ 5` AND `(actual − median) × 30 ≥ $10`. ~25 lines. Against Aug 9: median $0.02, scale floored at $0.01, z ≈ 172, projection $52/mo — fires.
6. **Recommended hourly detector: Poisson-scaled threshold plus a tabular CUSUM on hourly cost-equivalent units.** Immediate on `x > λ̂ + 5√λ̂ AND x ≥ 3λ̂`; sustained drift via CUSUM with `k = 0.5√λ̂`, `h = 5√λ̂` (NIST rule of thumb). √λ scaling is the correct dispersion for counts; Gaussian z is wrong at low counts.
7. **Hourly cannot be the standalone primary detector.** Cost Explorer hourly granularity is opt-in, retains only **14 days**, and is **available within 48 hours**. CUR 2.0 supports hourly line items but the export **refreshes at most once per day**. Standalone hourly detection is therefore slower than daily detection, not faster.
8. **Do not model weekly seasonality below ~8 weeks of history.** S-H-ESD, RAD/RPCA and Prophet all require multiple complete cycles; RAD requires the series length be divisible by the frequency. A 28-day trailing median already spans four of each weekday and costs nothing.
9. **Cold start under 7 days: widen the dispersion floor, do not suppress.** With `n < 7` use `scale = max(MAD, 0.5·max(history), $0.01)` and report "learning, day n/7" in the CLI. Detection from day 3 with a wide band beats no detection for 10 days.
10. **A deploy annotates an alert; it never suppresses one.** The Aug-9 spike *was* a known-cause ocel teardown sweep, and it was the alert that mattered. On overlap with an ocel-recorded deploy window, switch the gate from projected-monthly to one-off absolute (default $1) and label it attributed — then escalate back to unexplained severity if the same attributed anomaly recurs on ≥3 days in a rolling 7.

## What the providers actually do

### AWS Cost Anomaly Detection

AWS describes machine-learning models over **net unblended cost**, evaluated "approximately three times a day" after billing data is processed. It reads Cost Explorer, "which has a delay of up to 24 hours. As a result, it can take up to 24 hours to detect an anomaly after a usage occurs." A new monitor takes 24 hours to begin detecting, and "for a new service subscription, 10 days of historical service usage data is needed before anomalies can be detected for that service" ([manage-ad](https://docs.aws.amazon.com/cost-management/latest/userguide/manage-ad.html)).

Alerting is threshold-gated, and the thresholds are the interesting part. There are exactly two kinds: **absolute** ("an anomaly's total cost impact exceeds your chosen threshold") and **percentage** ("total impact percentage", defined as `(total cost impact / expected spend) × 100`). They can be combined with AND/OR. Cost impact is `actual spend − expected spend`. Crucially, impact percentage "cannot be calculated when expected spend is zero, so in those situations the value will show as 'N/A'" ([getting-started-ad](https://docs.aws.amazon.com/cost-management/latest/userguide/getting-started-ad.html)). An account going from nothing to something — ocel's cold-start case — is precisely where the relative trigger is undefined.

Sub-threshold anomalies are still detected and still listed in the console; the threshold is a notification filter, not a detection knob. The SNS payload exposes an `anomalyScore` (`currentScore`, `maxScore`, e.g. `0.47`) alongside `totalImpact`, `totalImpactPercentage` and ranked root causes. AWS does not publish the algorithm; the docs say only that it evaluates "weekly or monthly seasonality and natural growth". Severity is described qualitatively and non-monotonically: "a small spike with historically consistent spend is categorized as high severity … a big spike with irregular historical spend is categorized as low severity" — i.e. severity is a standardised deviation, not a dollar amount. That is the same shape as a median/MAD score.

In November 2025 AWS changed the detector to use "rolling 24-hour windows, comparing current costs against equivalent time periods from previous days", explicitly to remove "the delay in anomaly detection caused by comparing incomplete calendar-day costs against historical daily totals" ([what's new, 2025-11](https://aws.amazon.com/about-aws/whats-new/2025/11/aws-cost-anomaly-detection-accelerates-anomaly/)). Worth stealing: comparing partial-day-so-far against the same partial window on prior days is strictly better than comparing a partial day to a full-day baseline.

### Azure

Azure is the only provider that publishes its model. "The anomaly detection model is a univariate time-series, unsupervised prediction, and reconstruction-based model that uses 60 days of historical usage for training, then forecasts expected usage for the day. Anomaly detection forecasting uses a deep learning algorithm called WaveNet." Evaluation is **daily**, at **subscription scope only**, and runs "36 hours after the end of the day (UTC) to ensure a complete data set is available". A day is anomalous if normalized usage "falls outside the expected range based on a predetermined confidence interval" ([analyze-unexpected-charges](https://learn.microsoft.com/en-us/azure/cost-management-billing/understand/analyze-unexpected-charges)). Alert emails fire once, at detection, with a limit of five alert rules per subscription.

A 60-day WaveNet is the maximal version of the approach ocel cannot take: it requires two months of history, it is not explainable in a CLI line, and it is not expressible in 40 lines.

### Google Cloud

GCP splits into two products. **Standard anomalies** are project-level, detected next-day; users configure a **cost impact threshold** (a currency value to two decimal places) and a **deviation percentage**, and if unset these "automatically be set based on your usage patterns". **Early anomalies** are daily service-level signals with alerts "within 20 to 40 minutes from the time of usage", but their thresholds are "system-defined and can't be configured" ([manage-anomalies](https://docs.cloud.google.com/billing/docs/how-to/manage-anomalies)). Early anomalies and the paired spend caps are limited in preview to Gemini API, Agent Platform, Cloud Run and Cloud Run Functions — a single project and a single service, fixed monthly timeframe ([Google Cloud blog](https://cloud.google.com/blog/topics/cost-management/new-early-anomalies-and-spend-caps-on-google-cloud-budgets)). Google claims the baseline is "an expected seasonal baseline of daily service-level costs" built automatically; it does not publish a minimum history window.

The design lesson from GCP is the **spend cap**: near-real-time enforcement that "restricts further cost-incurring usage" without deleting data, reversible in one click. That is the same shape as the map's `stop` tier, and it is the only vendor precedent for non-destructive, reversible enforcement.

## What the FinOps vendors do

### Vantage — the most useful primary source

Vantage publishes its full filter chain, which is unusually candid ([docs.vantage.sh/cost_anomaly_alerts](https://docs.vantage.sh/cost_anomaly_alerts)). A forecast model trains on up to 6 months of **daily** cost grouped by provider/service/cost-category; days where actual exceeds the forecast's upper bound become candidates. Six filters then suppress candidates:

1. cost below **$5**
2. cost below **0.5% of the report's total daily cost**
3. not more than **20% above the trailing 7-day average**
4. not greater than the previous day
5. back-to-back suppression (same series within 7 days)
6. fewer than **12 days** of data for the series

The alert threshold is separate again: `trend = anomaly amount − trailing 7-day average`, and the alert delivers only if `trend ≥ threshold`. Vantage's own docs concede the filters "create a conservative system that may suppress legitimate spikes on broad reports while surfacing them on narrower ones".

Filters 1, 2 and 6 each independently kill the Aug-9 case. Filter 5 (back-to-back suppression) is worth noting as the only published prior art for repeat suppression — and it is the wrong polarity for ocel, where a repeating anomaly is *more* alarming, not less.

### CloudZero

Hourly granularity, automatic thresholds "based on a sliding scale tied to the previous 30 days of spend", manually overridable as "a custom percentage of the View's average daily spend" — the worked example is a $1000/day view at 50% giving a $500 threshold, triggered when an element's spend increases by that much over 24 hours ([docs.cloudzero.com](https://docs.cloudzero.com/docs/anomaly-detection)). Cost impact is `actual − expected`. The algorithm is not disclosed; the docs published under "statistical modeling to solve a time-series problem" name no method. A percentage-of-average-daily-spend threshold is scale-relative and would, at ocel's scale, reduce to noise: 50% of $0.02 is a cent.

### Kubecost

The most honest definition of the three: an anomaly is "an increase or decrease in cost greater than some threshold (outlier threshold) as compared to the mean of the cost over the previous X days (lookback window)", with three configurable knobs — **Outlier Threshold** (whole-number percentage deviation from the mean), **Lookback Window** (days), and **Minimum Cost** ("ignore anomalies with a cost below this threshold to avoid an overly noisy anomaly detection report") ([IBM/Kubecost docs](https://www.ibm.com/docs/en/SSW0JQG_2.x/using-kubecost/navigating-the-kubecost-ui/anomaly-detection.html)). No defaults are published. Note the *mean*, not the median — one prior spike poisons the baseline for the whole lookback window. This is the exact failure MAD exists to fix.

## The statistics, judged against ocel's four constraints

**Median / MAD.** Leys et al. (2013, *JESP* 49:764–766) is the canonical argument: mean and standard deviation are themselves sensitive to the outliers they are meant to find; compute the median, take the median of absolute deviations, multiply by **1.4826** under a normality assumption, and threshold at **2.5 MAD** as a reasonable default ([ULB copy](https://dipot.ulb.ac.be/dspace/bitstream/2013/139499/1/Leys_MAD_final-libre.pdf)). For daily cost this is the right primitive: it is 5 lines, it survives the previous spike being in the window, and its score reads out as "87× the trailing rate" in a CLI. Its one failure mode is the degenerate one — a flat series has MAD = 0 and every deviation is infinitely anomalous — which is why a dispersion floor is not optional.

**S-H-ESD (Twitter).** Seasonal decomposition to strip trend and seasonality, then Generalized ESD using median and MAD rather than mean and standard deviation, so seasonal spikes do not become anomalies ([Vallis, Hochenbaum, Kejariwal, arXiv:1704.07706](https://arxiv.org/abs/1704.07706); [github.com/twitter/AnomalyDetection](https://github.com/twitter/AnomalyDetection)). Parameters: `max_anoms`, `alpha`, `direction`, `longterm`, `piecewise_median_period_weeks`. It was built for minutely production metrics over months, and its own README concedes "trend extraction in the presence of anomalies is non-trivial". `max_anoms` as a *proportion* is itself a poor fit: it presumes anomalies are a stable fraction of the series. The robust core (median + MAD after decomposition) is worth taking; the decomposition is not, at ocel's history lengths.

**Netflix RAD / RPCA.** Robust PCA separating a low-rank component, noise and outliers by iterated SVD with thresholding ([Netflix/Surus](https://github.com/Netflix/Surus)). The R signature requires a `frequency` and the input vector's "length should be divisible by frequency" — it reshapes into a frequency × cycles matrix, so it needs whole cycles, ideally many. It also wants stationarity (Augmented Dickey-Fuller, auto-differencing) and optional normalisation ([AnomalyDetection.rpca.Rd](https://github.com/Netflix/Surus/blob/master/resources/R/RAD/man/AnomalyDetection.rpca.Rd)). Disqualified by cold start and by the 40-line budget.

**Prophet.** Its own docs treat outliers as an input problem, not an output: outliers make Prophet "fit trend changes" and inflate uncertainty intervals, extreme outliers corrupt seasonality so their effect "reverberates into the future forever", and "the best way to handle outliers is to remove them" ([Prophet docs](https://facebook.github.io/prophet/docs/outliers.html)). Using a forecaster whose documented advice is to remove anomalies first, as an anomaly detector, requires a robustification step that is more code than the detector it replaces.

**EWMA.** `EWMA_t = λY_t + (1−λ)EWMA_{t−1}`, λ typically 0.2–0.3, limits at `EWMA_0 ± L·s_ewma` with L ≈ 3; its advantage is sensitivity "to a small or gradual drift" where a Shewhart chart reacts only to the last point ([NIST 6.3.2.4](https://www.itl.nist.gov/div898/handbook/pmc/section3/pmc324.htm)). The wrong tool for a single 87× day — EWMA is designed to blunt exactly that — but the right tool for the *other* failure the map describes, spend creeping up over weeks.

**CUSUM.** `S_hi(i) = max(0, S_hi(i−1) + x_i − μ₀ − k)`, out of control when `S_hi > h`; rule of thumb "choose k to be half the δ shift … and h to be around 4 or 5" standard-deviation units ([NIST 6.3.2.3](https://www.itl.nist.gov/div898/handbook/pmc/section3/pmc323.htm)). Poisson variants are standard for count data (Lucas 1985). This is the right second stage for hourly request counts, where a sustained 2× rate that never spikes is exactly the sweep pathology and a single-point rule never sees it.

## Recommended detectors

### Daily (primary, standalone)

```go
// hist: trailing up to 28 daily costs, most recent last. cost: today.
func daily(hist []float64, cost, monthlyFloor float64) (fire bool, z, projected float64) {
	n := len(hist)
	if n < 3 {
		return false, 0, 0 // learning
	}
	m := median(hist)
	dev := make([]float64, n)
	for i, h := range hist {
		dev[i] = math.Abs(h - m)
	}
	scale := 1.4826 * median(dev)
	rel := 0.10
	if n < 7 {
		rel = 0.50 // cold start: wider band, fewer false positives
		scale = math.Max(scale, 0.5*maxOf(hist))
	}
	scale = math.Max(scale, math.Max(rel*m, 0.01)) // 0.01 = billing quantum
	z = (cost - m) / scale
	projected = (cost - m) * 30
	return z >= 5 && projected >= monthlyFloor, z, projected
}
```

Parameters: window 28 days; `z ≥ 5`; relative dispersion floor 10% of the median (50% during cold start); absolute dispersion floor $0.01; `monthlyFloor` default **$10/month**, user-settable in `ocel cost init`.

Behaviour on the dogfood case: `m = 0.02`, MAD collapses to near zero, `scale = max(≈0, 0.002, 0.01) = 0.01`, `z = 172`, `projected = $51.60` — fires, and the CLI line writes itself: *"Aug 9 — $1.74 vs $0.02 expected (87×). +$52/mo if this is the new rate."*

**False-positive story.** The z-gate alone is useless at this scale — a flat $0.02 series makes a $0.06 day a 4σ event. The materiality gate is what carries: a $0.05 excess projects to $1.50/mo and is silently dropped. The gate is a *rate* projection, not a dollar floor, which is exactly what lets $1.74 through while $5 floors (Vantage) and percent-of-daily-spend thresholds (CloudZero) do not. Expected volume: one alert per genuine step change in run rate, because condition 2 requires the excess to be worth ≥ $10/mo and condition 1 requires it to be unlike the last four weeks. Re-firing on consecutive days is intended, not noise — a step change that persists is a step change.

**Weekly cycle.** Not modelled. Twenty-eight days spans four of each weekday, so a weekday effect widens the MAD rather than producing false positives — the cost is sensitivity, not correctness. Revisit only with ≥ 56 days, and only if the ratio of max to min weekday median exceeds 1.5; the fix then is a per-weekday median over 4 samples with the pooled MAD, not a decomposition.

### Hourly (managed mode, or CUR-hourly backfill)

Run on **cost-equivalent units** (counts × unit price) so the same materiality gate applies. Counts are the honest variable and their dispersion is √λ, not σ:

```go
// x: this hour's cost-equivalent. hist: trailing 168 hourly values. s: persisted CUSUM.
func hourly(x float64, hist []float64, s *float64, unit float64) bool {
	lam := math.Max(trimmedMean(hist, 0.1), unit) // unit = one billing quantum
	sd := math.Sqrt(lam * unit)                   // Poisson-scaled, in cost units
	if x > lam+5*sd && x >= 3*lam {
		*s = 0
		return true
	}
	*s = math.Max(0, *s+(x-lam-0.5*sd))
	if *s > 5*sd {
		*s = 0
		return true
	}
	return false
}
```

The first clause is the burst rule; requiring both `+5σ` and `≥3×` prevents a low-λ hour firing on a handful of extra requests. The second is a tabular CUSUM with NIST's `k = δ/2`, `h ≈ 5` in σ units, catching a sustained ~1σ rate shift that never produces a single anomalous hour — the sweep-that-never-stops case.

**Where the data comes from matters more than the maths.** Cost Explorer hourly granularity is opt-in, covers only "the past 14 days", and "data at hourly granularity is available within 48 hours" ([ce-services-hourly](https://docs.aws.amazon.com/cost-management/latest/userguide/ce-services-hourly.html)); each paginated Cost Explorer API request costs **$0.01** ([ce-what-is](https://docs.aws.amazon.com/cost-management/latest/userguide/ce-what-is.html)). CUR 2.0 offers hourly line items, but "Data export refresh cadence … the only option available is Daily — export is refreshed up to one time per day" ([dataexports-create-standard](https://docs.aws.amazon.com/cur/latest/userguide/dataexports-create-standard.html)). So hourly billing data buys *resolution and attribution*, never *latency*. Sub-day alerting requires ocel's own meters, which exist only when ocel deploys. Standalone, the hourly detector runs as a backfill pass that sharpens the daily alert's root cause, not as the thing that fires first.

## The deploy rule

**A deploy annotates; it never suppresses.** The justification is in this repo's own evidence: the Aug-9 spike *was* a known-cause event — the teardown/preview-rm sweep — and it is the alert the design exists to produce. A suppress-on-known-cause rule would have suppressed it.

The rule, stated for implementation:

1. ocel records every deploy, teardown and sweep it runs as an event with a window `[start, end + T]` (`T` = 2h for hourly, the whole UTC day for daily) and a label. Only events **ocel itself ran** qualify; provider-side or human changes have no trustworthy window and must never suppress.
2. A fired anomaly whose window overlaps a recorded event is labelled `attributed(<event>)` instead of `unexplained`. It still fires, still appears, still notifies.
3. For an attributed anomaly, the materiality gate switches from projected-monthly (`impact × 30 ≥ $10`) to **one-off absolute** (`impact ≥ $1`, default). Multiplying a one-shot deploy cost by 30 asserts a recurrence that is not happening; the projection would be a lie, and a lie in the alert text is worse than a missed cent.
4. **Repeat escalation.** If the same attributed anomaly fires on ≥ 3 days in a rolling 7, it is re-labelled `unexplained` at full severity. The deploy has become the baseline, and that — not the individual spike — is the bug. This is deliberately the opposite polarity to Vantage's back-to-back suppression, which would have hidden the ocel case entirely.
5. Never learn suppression from user feedback silently. AWS offers an assessment ("Not an issue" / "Accurate anomaly") that feeds its model; ocel should record the same signal but keep it advisory and visible, not a hidden filter.

## Open questions

- **The $10/month materiality default is taste, not fact.** No vendor publishes a run-rate-projection gate to compare against; every published floor is an absolute daily dollar amount. The number should be settable at `ocel cost init` and probably defaults relative to a declared budget rather than absolutely.
- **`z ≥ 5` is not calibrated against real data.** With a floored dispersion the score is no longer a distributional quantity, so the usual 2.5-MAD (Leys) or ESD α arguments do not transfer. It needs a replay against 90 days of the dogfood account before it is more than a guess.
- **Platform-cost series may be structurally undetectable at the account level.** The map records that shared state/artifact buckets bill at bucket level and no tag scheme attributes them. Whether the daily detector fires usefully on a *bucket-level* series, or only on the account total, is an empirical question for the prototype.
- **AWS's algorithm remains unpublished.** The anomaly score semantics (`currentScore`/`maxScore`, range apparently 0–1) are visible in the SNS payload but undocumented; the severity description implies a standardised-deviation score, which is inference, not a cited fact.
- **Poisson vs negative binomial for hourly counts.** Sweep traffic is over-dispersed relative to Poisson (bursts arrive in correlated batches), so √λ likely understates the true dispersion and the hourly detector may over-fire. Verified over-dispersion would argue for a negative-binomial scale, which is more parameters than the 40-line budget allows.
- **GCP's history requirement could not be verified.** Secondary sources claim Google removed a prior six-month requirement so "new projects are protected from day one"; the primary docs state no minimum window either way.
