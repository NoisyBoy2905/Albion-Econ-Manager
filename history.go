package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Price history is an append-only log of every price we store, one JSON object
// per line (history.jsonl). It's meant to be read later by other projects (a
// market simulator), so the file is never rewritten, only appended to.

var exportHistFlag = flag.Bool("export-history", false, "write history-export.csv from history.jsonl, then exit")

// HistoryRecord is one line in history.jsonl.
type HistoryRecord struct {
	Time    string  `json:"time"` // RFC3339
	Item    string  `json:"item"`
	Quality int     `json:"quality"`
	City    string  `json:"city"` // zone ID, like "1002"
	Side    string  `json:"side"` // "sell" or "buy"
	Price   int64   `json:"price"`
	Amount  int     `json:"amount"`
	Levels  []Level `json:"levels"` // the whole order list, for our own scans
	Source  string  `json:"source"` // "own" or "public"
}

// History writes records to the log. It opens the file once and buffers
// writes, flushing after each page of orders.
type History struct {
	mu   sync.Mutex
	path string
	f    *os.File
	w    *bufio.Writer
	last map[string]string // item|quality|city|side -> last signature, for dedup
}

func NewHistory(path string) *History {
	h := &History{path: path, last: map[string]string{}}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		h.f, h.w = f, bufio.NewWriter(f)
	}
	return h
}

func levelsText(ls []Level) string {
	parts := make([]string, 0, len(ls))
	for _, l := range ls {
		parts = append(parts, strconv.FormatInt(l.Price, 10)+"x"+strconv.Itoa(l.Amount))
	}
	return strings.Join(parts, ";")
}

// dedupKey and recordSig together decide whether a record is a change worth
// logging: same key + same signature as last time means nothing changed.
func dedupKey(item string, quality int, city, side string) string {
	return item + "|" + strconv.Itoa(quality) + "|" + city + "|" + side
}

func recordSig(price int64, amount int, levels []Level) string {
	return strconv.FormatInt(price, 10) + "x" + strconv.Itoa(amount) + "|" + levelsText(levels)
}

// record appends one record, unless nothing changed since the last record for
// this item/quality/city/side.
func (h *History) record(r HistoryRecord) {
	if h == nil || h.w == nil {
		return
	}
	k := dedupKey(r.Item, r.Quality, r.City, r.Side)
	sig := recordSig(r.Price, r.Amount, r.Levels)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.last[k] == sig {
		return
	}
	h.last[k] = sig
	if data, err := json.Marshal(r); err == nil {
		h.w.Write(data)
		h.w.WriteByte('\n')
	}
}

// seedFrom primes the dedup map from prices already in the book, so that after
// a restart a public download doesn't re-log prices whose value hasn't changed
// (the API's dates advance each time, which would otherwise look like news).
// The signatures must match exactly what record() would produce for the same
// unchanged price, for own scans (full levels) and public prices (no amounts).
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

func (h *History) flush() {
	if h == nil || h.w == nil {
		return
	}
	h.mu.Lock()
	h.w.Flush()
	h.mu.Unlock()
}

// Close flushes and closes the log, for a clean shutdown.
func (h *History) Close() {
	if h == nil || h.w == nil {
		return
	}
	h.mu.Lock()
	h.w.Flush()
	h.f.Close()
	h.mu.Unlock()
}

// scanHistory reads history.jsonl line by line, calling fn for each record.
func scanHistory(path string, fn func(HistoryRecord)) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	s := bufio.NewScanner(in)
	s.Buffer(make([]byte, 64*1024), 8*1024*1024) // allow long level lists
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

// exportHistory writes history.jsonl out as a flat CSV, one row per record,
// with the levels as a short text like "2100x4;2600x10".
func exportHistory(histPath, outPath string) (int, error) {
	out, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	w := csv.NewWriter(out)
	w.Write([]string{"time", "item", "quality", "city", "side", "price", "amount", "levels", "source"})
	n := 0
	err = scanHistory(histPath, func(r HistoryRecord) {
		w.Write([]string{r.Time, r.Item, strconv.Itoa(r.Quality), r.City, r.Side,
			strconv.FormatInt(r.Price, 10), strconv.Itoa(r.Amount), levelsText(r.Levels), r.Source})
		n++
	})
	w.Flush()
	if err != nil {
		return n, err
	}
	return n, w.Error()
}

// historyPoint is one price at one time, for the chart.
type historyPoint struct {
	Time  string `json:"time"`
	Side  string `json:"side"`
	Price int64  `json:"price"`
}

// pointsFor returns the sell and buy prices over time for one item in one city.
func pointsFor(histPath, item string, quality int, city string) []historyPoint {
	out := []historyPoint{}
	scanHistory(histPath, func(r HistoryRecord) {
		if r.Item == item && r.Quality == quality && r.City == city {
			out = append(out, historyPoint{r.Time, r.Side, r.Price})
		}
	})
	return out
}

// runExportHistoryFlag handles -export-history and reports whether it ran.
func runExportHistoryFlag() bool {
	if !*exportHistFlag {
		return false
	}
	n, err := exportHistory("history.jsonl", "history-export.csv")
	if err != nil {
		fmt.Println("Couldn't export history:", err)
	} else {
		fmt.Printf("Wrote history-export.csv (%d rows)\n", n)
	}
	return true
}
