package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Price history, kept small. Raw records (one JSON object per line) live in
// history/raw/<day>.jsonl for the last 3 days only. Each day is also boiled
// down to one summary per item+quality+city+side in history/daily/<day>.json
// (min, median, max, snapshots, last, and the amount listed for own scans),
// kept for 90 days. Troll/suspicious prices are left out of the summaries.
// Everything is in UTC. stats.go reads the daily summaries to learn what's
// "normal". The layout must build and run on Linux too (no pcap / Windows).

var exportHistFlag = flag.Bool("export-history", false, "write history-export.csv from the history, then exit")

const (
	rawDays   = 3  // keep this many days of raw records
	dailyDays = 90 // keep this many days of daily summaries
	snapDays  = 7  // keep this many days of flip snapshots (see stats.go)
)

// HistoryRecord is one line in a raw history file.
type HistoryRecord struct {
	Time       string  `json:"time"` // RFC3339, UTC
	Item       string  `json:"item"`
	Quality    int     `json:"quality"`
	City       string  `json:"city"` // zone ID, like "1002"
	Side       string  `json:"side"` // "sell" or "buy"
	Price      int64   `json:"price"`
	Amount     int     `json:"amount"`
	Levels     []Level `json:"levels"`           // the whole order list, for our own scans
	Source     string  `json:"source"`           // "own" or "public"
	Suspicious bool    `json:"suspicious"`       // likely a troll/junk price
	Reason     string  `json:"reason,omitempty"` // why, if suspicious
}

// DaySum is one day's summary for one item+quality+city+side.
type DaySum struct {
	Day     string `json:"day"` // YYYY-MM-DD, UTC
	Item    string `json:"item"`
	Quality int    `json:"quality"`
	City    string `json:"city"`
	Side    string `json:"side"`
	Min     int64  `json:"min"`
	Median  int64  `json:"median"`
	Max     int64  `json:"max"`
	Count   int    `json:"count"` // snapshots seen
	Last    int64  `json:"last"`
	Listed  int    `json:"listed"` // total amount listed, own scans only
}

// History writes raw records and keeps today's summaries in memory, flushing
// both to disk. It opens one raw file per UTC day.
type History struct {
	mu    sync.Mutex
	dir   string
	raw   *os.File
	rawW  *bufio.Writer
	today string             // the day the open raw file belongs to
	last  map[string]string  // dedup signature per key, to keep raw small
	sum   map[string]*DaySum // today's summaries
	vals  map[string][]int64 // today's prices per key, for the median
}

func NewHistory(dir string) *History {
	h := &History{dir: dir, last: map[string]string{}, sum: map[string]*DaySum{}, vals: map[string][]int64{}}
	os.MkdirAll(filepath.Join(dir, "raw"), 0755)
	os.MkdirAll(filepath.Join(dir, "daily"), 0755)
	os.MkdirAll(filepath.Join(dir, "flips"), 0755)
	convertOld(dir, dir+".jsonl") // one-time migration of the old single file
	now := time.Now().UTC()
	rotate(dir, now)
	h.openRaw(now)
	h.loadToday(now)
	return h
}

func (h *History) openRaw(now time.Time) {
	h.today = now.Format("2006-01-02")
	if f, err := os.OpenFile(filepath.Join(h.dir, "raw", h.today+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		h.raw, h.rawW = f, bufio.NewWriter(f)
	}
}

// loadToday resumes today's summary if the process restarted mid-day, so the
// day's figures aren't lost. The median is re-seeded from the stored median.
func (h *History) loadToday(now time.Time) {
	data, err := os.ReadFile(filepath.Join(h.dir, "daily", now.Format("2006-01-02")+".json"))
	if err != nil {
		return
	}
	m := map[string]*DaySum{}
	if json.Unmarshal(data, &m) == nil {
		h.sum = m
		for k, s := range m {
			h.vals[k] = []int64{s.Median}
		}
	}
}

func levelsText(ls []Level) string {
	parts := make([]string, 0, len(ls))
	for _, l := range ls {
		parts = append(parts, strconv.FormatInt(l.Price, 10)+"x"+strconv.Itoa(l.Amount))
	}
	return strings.Join(parts, ";")
}

func dedupKey(item string, quality int, city, side string) string {
	return item + "|" + strconv.Itoa(quality) + "|" + city + "|" + side
}

func recordSig(price int64, amount int, levels []Level) string {
	return strconv.FormatInt(price, 10) + "x" + strconv.Itoa(amount) + "|" + levelsText(levels)
}

func listedAmount(r HistoryRecord) int {
	n := 0
	for _, l := range r.Levels {
		n += l.Amount
	}
	return n
}

// record adds one price. It always updates today's summary (unless the price is
// a troll/junk one), but only writes to the raw file when something changed, to
// keep the raw files small.
func (h *History) record(r HistoryRecord) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rollIfNeeded()
	if !r.Suspicious {
		addToSummary(h.sum, h.vals, h.today, r)
	}
	k := dedupKey(r.Item, r.Quality, r.City, r.Side)
	sig := recordSig(r.Price, r.Amount, r.Levels)
	if h.last[k] == sig {
		return
	}
	h.last[k] = sig
	if h.rawW != nil {
		if data, err := json.Marshal(r); err == nil {
			h.rawW.Write(data)
			h.rawW.WriteByte('\n')
		}
	}
}

// addToSummary folds one record into the day's summary for its key.
func addToSummary(sum map[string]*DaySum, vals map[string][]int64, day string, r HistoryRecord) {
	k := dedupKey(r.Item, r.Quality, r.City, r.Side)
	s := sum[k]
	if s == nil {
		s = &DaySum{Day: day, Item: r.Item, Quality: r.Quality, City: r.City, Side: r.Side, Min: r.Price, Max: r.Price}
		sum[k] = s
	}
	if r.Price < s.Min {
		s.Min = r.Price
	}
	if r.Price > s.Max {
		s.Max = r.Price
	}
	s.Last = r.Price
	s.Count++
	if r.Source == "own" {
		s.Listed = listedAmount(r)
	}
	vals[k] = append(vals[k], r.Price)
	s.Median = median(vals[k])
}

// rollIfNeeded closes out the day when the UTC date changes.
func (h *History) rollIfNeeded() {
	day := time.Now().UTC().Format("2006-01-02")
	if day == h.today {
		return
	}
	h.writeDailyLocked()
	if h.rawW != nil {
		h.rawW.Flush()
	}
	if h.raw != nil {
		h.raw.Close()
	}
	h.sum, h.vals, h.last = map[string]*DaySum{}, map[string][]int64{}, map[string]string{}
	now := time.Now().UTC()
	h.openRaw(now)
	rotate(h.dir, now)
}

func (h *History) flush() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rawW != nil {
		h.rawW.Flush()
	}
	h.writeDailyLocked()
}

func (h *History) writeDailyLocked() {
	if len(h.sum) == 0 {
		return
	}
	if data, err := json.Marshal(h.sum); err == nil {
		os.WriteFile(filepath.Join(h.dir, "daily", h.today+".json"), data, 0644)
	}
}

func (h *History) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rawW != nil {
		h.rawW.Flush()
	}
	h.writeDailyLocked()
	if h.raw != nil {
		h.raw.Close()
	}
}

// seedFrom primes the dedup map from prices already in the book, so a restart
// doesn't re-log prices whose value hasn't changed.
func (h *History) seedFrom(b *Book) {
	if h == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range b.Prices {
		if p.Sell > 0 {
			amount, levels := 0, []Level(nil)
			if !p.SellPublic && len(p.SellLevels) > 0 {
				amount, levels = p.SellLevels[0].Amount, p.SellLevels
			}
			h.last[dedupKey(p.Item, p.Quality, p.City, "sell")] = recordSig(p.Sell, amount, levels)
		}
		if p.Buy > 0 {
			amount, levels := 0, []Level(nil)
			if !p.BuyPublic && len(p.BuyLevels) > 0 {
				amount, levels = p.BuyLevels[0].Amount, p.BuyLevels
			}
			h.last[dedupKey(p.Item, p.Quality, p.City, "buy")] = recordSig(p.Buy, amount, levels)
		}
	}
}

// --- files and rotation ---

func dayOf(name, ext string) (string, time.Time, bool) {
	day := strings.TrimSuffix(name, ext)
	t, err := time.Parse("2006-01-02", day)
	return day, t, err == nil
}

// rotate summarises then deletes raw files older than rawDays, and deletes
// daily summaries and flip snapshots past their retention.
func rotate(dir string, now time.Time) {
	rawDir := filepath.Join(dir, "raw")
	if entries, err := os.ReadDir(rawDir); err == nil {
		cutoff := now.AddDate(0, 0, -rawDays)
		for _, e := range entries {
			day, t, ok := dayOf(e.Name(), ".jsonl")
			if !ok || !t.Before(cutoff) {
				continue
			}
			dailyPath := filepath.Join(dir, "daily", day+".json")
			if _, err := os.Stat(dailyPath); err != nil {
				summariseRawFile(filepath.Join(rawDir, e.Name()), dailyPath, day)
			}
			os.Remove(filepath.Join(rawDir, e.Name()))
		}
	}
	pruneOld(filepath.Join(dir, "daily"), ".json", now.AddDate(0, 0, -dailyDays))
	pruneOld(filepath.Join(dir, "flips"), ".jsonl", now.AddDate(0, 0, -snapDays))
}

func pruneOld(dir, ext string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if _, t, ok := dayOf(e.Name(), ext); ok && t.Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// summariseRawFile boils one raw file down to a daily summary file.
func summariseRawFile(rawPath, dailyPath, day string) {
	sum := map[string]*DaySum{}
	vals := map[string][]int64{}
	scanJSONL(rawPath, func(r HistoryRecord) {
		if !r.Suspicious {
			addToSummary(sum, vals, day, r)
		}
	})
	if len(sum) > 0 {
		if data, err := json.Marshal(sum); err == nil {
			os.WriteFile(dailyPath, data, 0644)
		}
	}
}

// convertOld turns the old single history.jsonl into daily summaries, then
// renames it to history.jsonl.old (keeping it, not deleting it).
func convertOld(dir, oldPath string) {
	if _, err := os.Stat(oldPath); err != nil {
		return
	}
	byDay := map[string]map[string]*DaySum{}
	byDayVals := map[string]map[string][]int64{}
	scanJSONL(oldPath, func(r HistoryRecord) {
		if r.Suspicious || len(r.Time) < 10 {
			return
		}
		day := r.Time[:10]
		if byDay[day] == nil {
			byDay[day], byDayVals[day] = map[string]*DaySum{}, map[string][]int64{}
		}
		addToSummary(byDay[day], byDayVals[day], day, r)
	})
	for day, sum := range byDay {
		if data, err := json.Marshal(sum); err == nil {
			os.WriteFile(filepath.Join(dir, "daily", day+".json"), data, 0644)
		}
	}
	os.Rename(oldPath, oldPath+".old")
}

// scanJSONL reads a .jsonl file, calling fn for each record.
func scanJSONL(path string, fn func(HistoryRecord)) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	s := bufio.NewScanner(in)
	s.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for s.Scan() {
		line := s.Bytes()
		if len(line) == 0 {
			continue
		}
		var r HistoryRecord
		if json.Unmarshal(line, &r) == nil {
			fn(r)
		}
	}
	return s.Err()
}

// rawFiles returns the raw .jsonl paths, oldest day first.
func rawFiles(dir string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, "raw"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = filepath.Join(dir, "raw", n)
	}
	return out
}

// exportHistory writes the raw records (the last few days) out as a flat CSV.
func exportHistory(dir, outPath string) (int, error) {
	out, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	w := csv.NewWriter(out)
	w.Write([]string{"time", "item", "quality", "city", "side", "price", "amount", "levels", "source", "suspicious", "reason"})
	n := 0
	for _, path := range rawFiles(dir) {
		scanJSONL(path, func(r HistoryRecord) {
			w.Write([]string{r.Time, r.Item, strconv.Itoa(r.Quality), r.City, r.Side,
				strconv.FormatInt(r.Price, 10), strconv.Itoa(r.Amount), levelsText(r.Levels), r.Source,
				strconv.FormatBool(r.Suspicious), r.Reason})
			n++
		})
	}
	w.Flush()
	return n, w.Error()
}

// historyPoint is one price at one time, for the chart line.
type historyPoint struct {
	Time  string `json:"time"`
	Side  string `json:"side"`
	Price int64  `json:"price"`
}

// pointsFor returns the raw sell and buy prices over time for one item in one
// city (the last few days), for the chart line.
func pointsFor(dir, item string, quality int, city string) []historyPoint {
	out := []historyPoint{}
	for _, path := range rawFiles(dir) {
		scanJSONL(path, func(r HistoryRecord) {
			if r.Item == item && r.Quality == quality && r.City == city && !r.Suspicious {
				out = append(out, historyPoint{r.Time, r.Side, r.Price})
			}
		})
	}
	return out
}

func runExportHistoryFlag() bool {
	if !*exportHistFlag {
		return false
	}
	n, err := exportHistory("history", "history-export.csv")
	if err != nil {
		fmt.Println("Couldn't export history:", err)
	} else {
		fmt.Printf("Wrote history-export.csv (%d rows)\n", n)
	}
	return true
}
