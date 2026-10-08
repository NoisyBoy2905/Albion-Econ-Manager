package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// stats.go learns from the daily summaries: what price is "normal" for each
// item, how jumpy it is, which way it's trending, how far today's price is from
// normal, how reliable each flip has been, and how fast our own listings sell.
// It's plain statistics — a median here, a percentile there — so every number
// can be explained. It reads only files written by history.go, so it builds and
// runs on Linux with no packet capture.

const (
	statsWindow = 7 // learn from this many days of summaries
	minDays     = 3 // need at least this many days before showing a score
	minSnaps    = 5 // and at least this many snapshots for that item
)

// percentile returns the value at p (0..1) of a sorted slice, nearest-rank.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p*float64(len(sorted)-1) + 0.5)
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

func meanInt(xs []int64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum int64
	for _, x := range xs {
		sum += x
	}
	return float64(sum) / float64(len(xs))
}

// keyStat is what we learn about one item+quality+city+side.
type keyStat struct {
	Normal int64
	Swing  float64 // spread between day medians, as % of normal
	Trend  float64 // last 2 days vs the 5 before, as %
	Days   int
	Count  int
	Enough bool
}

func swingLabel(pct float64) string {
	switch {
	case pct < 8:
		return "steady"
	case pct < 25:
		return "normal"
	default:
		return "jumpy"
	}
}

// statFromMedians works out the stat for one key from its day medians (oldest
// first) and the total snapshot count.
func statFromMedians(dayMedians []int64, totalCount int) keyStat {
	s := keyStat{Days: len(dayMedians), Count: totalCount}
	if len(dayMedians) == 0 {
		return s
	}
	sorted := append([]int64(nil), dayMedians...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	s.Normal = median(sorted)
	if s.Normal > 0 {
		s.Swing = float64(percentile(sorted, 0.75)-percentile(sorted, 0.25)) / float64(s.Normal) * 100
	}
	// Trend: last up-to-2 days vs the up-to-5 before them.
	n := len(dayMedians)
	if n >= 3 {
		last := dayMedians[n-2:]
		priorStart := n - 7
		if priorStart < 0 {
			priorStart = 0
		}
		prior := dayMedians[priorStart : n-2]
		if pm := meanInt(prior); pm > 0 {
			s.Trend = (meanInt(last) - pm) / pm * 100
		}
	}
	s.Enough = s.Days >= minDays && s.Count >= minSnaps
	return s
}

// HistStats holds everything learned, ready for the window.
type HistStats struct {
	key  map[string]keyStat // dedupKey (item|quality|city|side) -> stat
	rel  map[string]float64 // flip/craft key -> reliability (0..1)
	relN int                // snapshots the reliability is based on
	days int                // distinct days of history in the window
}

// loadSummaries reads the daily summaries for the last `days` days.
func loadSummaries(dir string, now time.Time, days int) map[string][]DaySum {
	out := map[string][]DaySum{}
	for d := 0; d < days; d++ {
		day := now.AddDate(0, 0, -d).Format("2006-01-02")
		data, err := os.ReadFile(filepath.Join(dir, "daily", day+".json"))
		if err != nil {
			continue
		}
		m := map[string]DaySum{}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		for k, s := range m {
			out[k] = append(out[k], s)
		}
	}
	// oldest day first within each key
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool { return out[k][i].Day < out[k][j].Day })
	}
	return out
}

func loadStats(dir string, now time.Time) *HistStats {
	hs := &HistStats{key: map[string]keyStat{}, rel: map[string]float64{}}
	summaries := loadSummaries(dir, now, statsWindow)
	daysSeen := map[string]bool{}
	for k, days := range summaries {
		var medians []int64
		total := 0
		for _, d := range days {
			medians = append(medians, d.Median)
			total += d.Count
			daysSeen[d.Day] = true
		}
		hs.key[k] = statFromMedians(medians, total)
	}
	hs.days = len(daysSeen)
	hs.rel, hs.relN = reliabilityFromSnapshots(dir, now)
	return hs
}

func (h *HistStats) stat(item string, quality int, city, side string) (keyStat, bool) {
	if h == nil {
		return keyStat{}, false
	}
	s, ok := h.key[dedupKey(item, quality, city, side)]
	return s, ok && s.Enough
}

// reliability for a flip or craft key; ok only with enough snapshots.
func (h *HistStats) reliability(key string) (float64, bool) {
	if h == nil || h.relN < minSnaps {
		return 0, false
	}
	return h.rel[key], true
}

func (h *HistStats) learningDays() int {
	if h == nil {
		return 0
	}
	return h.days
}

// --- flip/craft reliability snapshots ---

func flipRelKey(item string, quality int, from, to, mode string) string {
	return item + "|" + strconv.Itoa(quality) + "|" + from + "|" + to + "|" + mode
}

func craftRelKey(item string, quality int) string {
	return "craft|" + item + "|" + strconv.Itoa(quality)
}

type snapLine struct {
	T string   `json:"t"`
	K []string `json:"k"` // keys that were profitable at this snapshot
}

// recordSnapshot appends one snapshot of which flips/crafts were profitable.
func recordSnapshot(dir string, now time.Time, keys []string) {
	path := filepath.Join(dir, "flips", now.UTC().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	if data, err := json.Marshal(snapLine{T: now.UTC().Format(time.RFC3339), K: keys}); err == nil {
		f.Write(append(data, '\n'))
	}
}

// reliabilityFromSnapshots reads the last snapDays of snapshots and returns, per
// key, the share of snapshots where it was profitable, plus the snapshot count.
func reliabilityFromSnapshots(dir string, now time.Time) (map[string]float64, int) {
	hits := map[string]int{}
	total := 0
	for d := 0; d < snapDays; d++ {
		day := now.AddDate(0, 0, -d).Format("2006-01-02")
		scanSnapshots(filepath.Join(dir, "flips", day+".jsonl"), func(s snapLine) {
			total++
			for _, k := range s.K {
				hits[k]++
			}
		})
	}
	rel := map[string]float64{}
	if total > 0 {
		for k, n := range hits {
			rel[k] = float64(n) / float64(total)
		}
	}
	return rel, total
}

func scanSnapshots(path string, fn func(snapLine)) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range splitLines(data) {
		var s snapLine
		if json.Unmarshal(line, &s) == nil {
			fn(s)
		}
	}
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// --- sell speed ---

// soldBetween sums how much the listed amount dropped between consecutive own
// scans (a drop means items sold). Rises (restocks) don't count.
func soldBetween(scans []HistoryRecord) int {
	sold, have := 0, false
	var prev int
	for _, r := range scans {
		if r.Source != "own" {
			continue
		}
		cur := listedAmount(r)
		if have && cur < prev {
			sold += prev - cur
		}
		prev, have = cur, true
	}
	return sold
}

// sellSpeed estimates items sold per day for one item in one city, from the raw
// own scans we have. Returns false if there isn't enough to tell.
func sellSpeed(dir, item string, quality int, city string, now time.Time) (float64, bool) {
	var scans []HistoryRecord
	for _, path := range rawFiles(dir) {
		scanJSONL(path, func(r HistoryRecord) {
			if r.Source == "own" && r.Side == "sell" && r.Item == item && r.Quality == quality && r.City == city {
				scans = append(scans, r)
			}
		})
	}
	if len(scans) < 2 {
		return 0, false
	}
	sort.Slice(scans, func(i, j int) bool { return scans[i].Time < scans[j].Time })
	sold := soldBetween(scans)
	first, err1 := time.Parse(time.RFC3339, scans[0].Time)
	last, err2 := time.Parse(time.RFC3339, scans[len(scans)-1].Time)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	days := last.Sub(first).Hours() / 24
	if days < 0.5 {
		days = 0.5 // avoid wild rates from scans minutes apart
	}
	return float64(sold) / days, true
}

// normalBand is the normal sell-price range for the chart's shaded area.
type normalRange struct {
	Low    int64 `json:"low"`
	Normal int64 `json:"normal"`
	High   int64 `json:"high"`
	OK     bool  `json:"ok"`
}

func normalBand(dir, item string, quality int, city string) normalRange {
	if dir == "" {
		return normalRange{}
	}
	days := loadSummaries(dir, time.Now().UTC(), statsWindow)[dedupKey(item, quality, city, "sell")]
	if len(days) < minDays {
		return normalRange{}
	}
	var medians []int64
	for _, d := range days {
		medians = append(medians, d.Median)
	}
	sort.Slice(medians, func(i, j int) bool { return medians[i] < medians[j] })
	return normalRange{Low: percentile(medians, 0.25), Normal: median(medians), High: percentile(medians, 0.75), OK: true}
}
