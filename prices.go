package main

import (
	"sort"
	"strings"
	"time"
)

// PriceRow is one line of the "all prices" list in the window.
type PriceRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Tier       string `json:"tier"`
	Quality    int    `json:"quality"`
	City       string `json:"city"`
	CityID     string `json:"cityId"`     // raw zone ID, for the history chart
	Sell       int64  `json:"sell"`       // cheapest sell order (0 if none)
	SellAmount int    `json:"sellAmount"` // how many at that price
	Buy        int64  `json:"buy"`        // best buy order (0 if none)
	BuyAmount  int    `json:"buyAmount"`
	AgeMin     int    `json:"ageMin"`
	SellPublic bool   `json:"sellPublic"`
	BuyPublic  bool   `json:"buyPublic"`
	Check      bool   `json:"check"` // best buy is suspiciously high: verify in game
}

// priceRow builds one row for a cleaned price entry.
func priceRow(p *Price, now time.Time, maxAge time.Duration) PriceRow {
	seen := p.SellSeen
	if p.BuySeen.After(seen) {
		seen = p.BuySeen
	}
	r := PriceRow{
		ID: p.Item, Name: itemName(p.Item), Tier: tier(p.Item), Quality: p.Quality,
		City: cityName(p.City), CityID: p.City, AgeMin: int(now.Sub(seen).Minutes()),
	}
	if p.Sell > 0 && now.Sub(p.SellSeen) <= maxAge {
		r.Sell, r.SellPublic = p.Sell, p.SellPublic
		if len(p.SellLevels) > 0 {
			r.SellAmount = p.SellLevels[0].Amount
		}
	}
	if p.Buy > 0 && now.Sub(p.BuySeen) <= maxAge {
		r.Buy, r.BuyPublic, r.Check = p.Buy, p.BuyPublic, p.check
		if len(p.BuyLevels) > 0 {
			r.BuyAmount = p.BuyLevels[0].Amount
		}
	}
	return r
}

// priceMatches reports whether a price is within maxAge and matches the search
// text (already lower-cased). An empty search matches everything.
func priceMatches(p *Price, now time.Time, maxAge time.Duration, search string) bool {
	if p.City == "" {
		return false
	}
	seen := p.SellSeen
	if p.BuySeen.After(seen) {
		seen = p.BuySeen
	}
	if now.Sub(seen) > maxAge {
		return false
	}
	if search == "" {
		return true
	}
	return strings.Contains(strings.ToLower(itemName(p.Item)), search) ||
		strings.Contains(strings.ToLower(p.Item), search) ||
		strings.Contains(strings.ToLower(cityName(p.City)), search)
}

// AllCount counts the prices within maxAge matching search, without building
// any rows — cheap enough to run every poll for the tab's total.
func (b *Book) AllCount(maxAge time.Duration, search string) int {
	prices, _ := b.clean(maxAge)
	now := time.Now()
	n := 0
	for _, p := range prices {
		if priceMatches(p, now, maxAge, search) {
			n++
		}
	}
	return n
}

// AllPage builds the matching rows (newest first), returns one page of them and
// the total count, so the window can show "showing X of Y" without the browser
// ever holding all ~100,000 prices.
func (b *Book) AllPage(maxAge time.Duration, search string, page, pageSize int) ([]PriceRow, int) {
	prices, _ := b.clean(maxAge)
	now := time.Now()
	out := []PriceRow{}
	for _, p := range prices {
		if priceMatches(p, now, maxAge, search) {
			out = append(out, priceRow(p, now, maxAge))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AgeMin != out[j].AgeMin {
			return out[i].AgeMin < out[j].AgeMin
		}
		return out[i].Name < out[j].Name
	})
	total := len(out)
	if pageSize <= 0 {
		return []PriceRow{}, total
	}
	start := page * pageSize
	if start < 0 || start >= total {
		return []PriceRow{}, total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return out[start:end], total
}

// All lists the newest prices in a known city, up to limit. Kept for callers
// (cloud mode, tests) that want a simple capped list.
func (b *Book) All(maxAge time.Duration, limit int) []PriceRow {
	rows, _ := b.AllPage(maxAge, "", 0, limit)
	return rows
}
