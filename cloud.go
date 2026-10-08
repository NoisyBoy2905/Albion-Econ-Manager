package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Cloud mode runs the public-price side with no packet capture: it downloads
// public prices, updates the history and stats (Phase 1), and writes static
// JSON for a website. It's meant to run on GitHub Actions every couple of hours
// so the site stays current even when the desktop sniffer is off. It builds and
// runs on Linux (no pcap / Windows).

// siteData is current.json: the top flips and crafts plus totals.
type siteData struct {
	Updated      string     `json:"updated"` // RFC3339, UTC
	FlipCount    int        `json:"flipCount"`
	CraftCount   int        `json:"craftCount"`
	PriceCount   int        `json:"priceCount"`
	LearningDays int        `json:"learningDays"`
	Flips        []flipJSON `json:"flips"`
	Crafts       []Craft    `json:"crafts"`
}

// runCloud downloads public prices from base at the given rate, updates the
// history under dataDir, and writes the website data under outDir. It returns an
// error (and leaves the old site data in place) if the download fails.
func runCloud(outDir, dataDir, base string, rate time.Duration) error {
	userAgent = "albion-econ-manager (github.com/NoisyBoy2905)"
	loadItemNames()
	loadRecipes()

	app := NewApp(filepath.Join(dataDir, "prices.json"))
	h := NewHistory(filepath.Join(dataDir, "history"))
	defer h.Close()
	app.hist, app.book.hist = h, h
	h.seedFrom(app.book)

	app.fetchPublic(base, rate) // blocks until the download finishes
	if st := app.publicState(); st.Error != "" {
		return fmt.Errorf("public price download failed, keeping old data: %s", st.Error)
	}
	app.book.Flush()
	h.flush()

	// One reliability snapshot per run, from the full flip/craft lists.
	flips := app.book.Flips(0.04, 6*time.Hour, false, 0, 0)
	crafts := app.book.Crafts(0.04, 0.152, 0, 6*time.Hour)
	app.lastSnap = time.Time{} // force a snapshot this run
	app.recordSnapshot(h.dir, flips, crafts)
	h.flush()

	return writeSite(outDir, app, h.dir, flips, crafts)
}

func writeSite(outDir string, app *App, histDir string, flips []Flip, crafts []Craft) error {
	now := time.Now().UTC()
	stats := loadStats(histDir, now)
	if err := os.MkdirAll(filepath.Join(outDir, "items"), 0755); err != nil {
		return err
	}

	// current.json: top 500 flips and crafts with the learned scores.
	topFlips := make([]flipJSON, 0, 500)
	for i, f := range flips {
		if i == 500 {
			break
		}
		topFlips = append(topFlips, flipRowJSON(f, stats))
	}
	for i := range crafts {
		crafts[i].Reliability, crafts[i].RelOK = stats.reliability(craftRelKey(crafts[i].ID, 1))
	}
	topCrafts := crafts
	if len(topCrafts) > 500 {
		topCrafts = topCrafts[:500]
	}
	prices := app.book.All(6*time.Hour, 1000000)
	cur := siteData{
		Updated: now.Format(time.RFC3339), FlipCount: len(flips), CraftCount: len(crafts),
		PriceCount: len(prices), LearningDays: stats.learningDays(), Flips: topFlips, Crafts: topCrafts,
	}
	if err := writeJSON(filepath.Join(outDir, "current.json"), cur); err != nil {
		return err
	}
	// prices.json: every current price, for in-browser search.
	if err := writeJSON(filepath.Join(outDir, "prices.json"), prices); err != nil {
		return err
	}

	// items/<ID>.json: 30 days of daily summaries, only rewritten when changed.
	byItem := map[string][]DaySum{}
	for _, days := range loadSummaries(histDir, now, 30) {
		for _, s := range days {
			byItem[s.Item] = append(byItem[s.Item], s)
		}
	}
	for item, summ := range byItem {
		sort.Slice(summ, func(i, j int) bool { return summ[i].Day < summ[j].Day })
		writeJSONIfChanged(filepath.Join(outDir, "items", item+".json"), summ)
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// writeJSONIfChanged only writes when the content differs, so unchanged item
// files aren't rewritten (and git sees no change).
func writeJSONIfChanged(path string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return
	}
	os.WriteFile(path, data, 0644)
}
