package main

import (
	"math"
	"time"
)

// quant.go adds market-statistics maths on top of the daily price summaries
// (history/daily): how much a price bounces around (volatility), how unusual
// today's price is (z-score), which way it's heading (trend), and how steady a
// flip's profit is. Everything is a small pure function so each piece can be
// read and tested on its own, and it runs on Linux in the cloud with no game
// reading and no Windows code. See docs/QUANT.md for plain-English explanations.
//
// Honest small samples: every metric needs enough data, and says "not enough"
// (ok == false) rather than inventing a number when it doesn't have it.

const quantWindow = 14 // days of daily medians the quant metrics look back over

// mean is the plain average of a list of numbers.
func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// stdev is the sample standard deviation: the typical distance of a value from
// the average. Small = the numbers cluster tightly; big = they're spread out.
// It divides by (n-1) because we're estimating from a sample, and is 0 when
// there are fewer than two numbers.
func stdev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := mean(xs)
	var ss float64
	for _, x := range xs {
		d := x - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}

// logReturns turns a run of daily prices into daily growth measured in logs:
// ln(today / yesterday). Logs are used so a +10% followed by a −10% cancels
// out properly and the maths adds up cleanly. A non-positive price (which can't
// have a log) breaks the chain, so that step is skipped.
func logReturns(medians []int64) []float64 {
	var out []float64
	for i := 1; i < len(medians); i++ {
		if medians[i-1] <= 0 || medians[i] <= 0 {
			continue
		}
		out = append(out, math.Log(float64(medians[i])/float64(medians[i-1])))
	}
	return out
}

// volatilityPct is how much the daily price typically moves, as a percent: the
// standard deviation of the daily log returns, times 100. It needs at least 5
// daily points. If the price never moved (std 0) it returns ok == false, since
// that usually means too few real updates rather than a genuinely frozen price.
func volatilityPct(medians []int64) (float64, bool) {
	if len(medians) < 5 {
		return 0, false
	}
	r := logReturns(medians)
	if len(r) < 2 {
		return 0, false
	}
	sd := stdev(r)
	if sd == 0 {
		return 0, false
	}
	return sd * 100, true
}

// volLabel turns a daily volatility percent into a plain word.
// Thresholds (documented in QUANT.md): under 5% steady, 5–15% normal, over
// 15% jumpy. A steady price is safer to flip; a jumpy one can swing against you.
func volLabel(pct float64) string {
	switch {
	case pct < 5:
		return "steady"
	case pct < 15:
		return "normal"
	default:
		return "jumpy"
	}
}

// QuantStat is the quant maths for one item+quality+city+side.
type QuantStat struct {
	Vol      float64 // daily volatility, %
	VolOK    bool
	VolLabel string
	Days     int // daily points it was worked out from
}

// loadQuant works out the quant metrics for every key from the last
// quantWindow days of daily summaries.
func loadQuant(dir string, now time.Time) map[string]QuantStat {
	out := map[string]QuantStat{}
	for k, days := range loadSummaries(dir, now, quantWindow) {
		medians := make([]int64, len(days))
		for i, d := range days {
			medians[i] = d.Median
		}
		q := QuantStat{Days: len(days)}
		if v, ok := volatilityPct(medians); ok {
			q.Vol, q.VolOK, q.VolLabel = v, true, volLabel(v)
		}
		out[k] = q
	}
	return out
}
