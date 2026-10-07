package main

import (
	"sort"
	"time"
)

// PriceRow is one line of the "all prices" list in the window.
type PriceRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Tier       string `json:"tier"`
	Quality    int    `json:"quality"`
	City       string `json:"city"`
	Sell       int64  `json:"sell"`       // cheapest sell order (0 if none)
	SellAmount int    `json:"sellAmount"` // how many at that price
	Buy        int64  `json:"buy"`        // best buy order (0 if none)
	BuyAmount  int    `json:"buyAmount"`
	AgeMin     int    `json:"ageMin"`
	SellPublic bool   `json:"sellPublic"`
	BuyPublic  bool   `json:"buyPublic"`
}

// All lists every price we've seen in a known city, newest first.
func (b *Book) All(maxAge time.Duration, limit int) []PriceRow {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	out := []PriceRow{}
	for _, p := range b.Prices {
		if p.City == "" {
			continue
		}
		seen := p.SellSeen
		if p.BuySeen.After(seen) {
			seen = p.BuySeen
		}
		if now.Sub(seen) > maxAge {
			continue
		}
		r := PriceRow{
			ID: p.Item, Name: itemName(p.Item), Tier: tier(p.Item), Quality: p.Quality,
			City: cityName(p.City), AgeMin: int(now.Sub(seen).Minutes()),
		}
		if p.Sell > 0 && now.Sub(p.SellSeen) <= maxAge {
			r.Sell, r.SellPublic = p.Sell, p.SellPublic
			if len(p.SellLevels) > 0 {
				r.SellAmount = p.SellLevels[0].Amount
			}
		}
		if p.Buy > 0 && now.Sub(p.BuySeen) <= maxAge {
			r.Buy, r.BuyPublic = p.Buy, p.BuyPublic
			if len(p.BuyLevels) > 0 {
				r.BuyAmount = p.BuyLevels[0].Amount
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AgeMin != out[j].AgeMin {
			return out[i].AgeMin < out[j].AgeMin
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
