package main

import (
	"encoding/json"
	"sort"
	"time"
)

// Recipes come from the game's own item data (web/recipes.json).
type Recipe struct {
	Makes     int     `json:"n"` // how many one craft makes
	Silver    int64   `json:"s"` // silver cost per craft
	Materials [][]any `json:"r"` // [item id, count, 1 if it can be returned]
	Category  string  `json:"c"`
	Value     float64 `json:"v"` // the game's "item value", used for the station fee
}

var recipes = map[string]Recipe{}

func loadRecipes() {
	data, err := webFiles.ReadFile("web/recipes.json")
	if err == nil {
		json.Unmarshal(data, &recipes)
	}
}

// Quote is the best price for an item across every city we've seen.
type Quote struct {
	Price  int64
	City   string
	Seen   time.Time
	Public bool
}

// bestPrices finds, for each item at Normal quality, the cheapest sell order
// and the highest buy order in any city.
func (b *Book) bestPrices(maxAge time.Duration) (cheapest, highest map[string]Quote) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	cheapest, highest = map[string]Quote{}, map[string]Quote{}
	for _, p := range b.Prices {
		if p.Quality > 1 || p.City == "" {
			continue
		}
		if p.Sell > 0 && now.Sub(p.SellSeen) <= maxAge {
			if q, ok := cheapest[p.Item]; !ok || p.Sell < q.Price {
				cheapest[p.Item] = Quote{p.Sell, p.City, p.SellSeen, p.SellPublic}
			}
		}
		if p.Buy > 0 && now.Sub(p.BuySeen) <= maxAge {
			if q, ok := highest[p.Item]; !ok || p.Buy > q.Price {
				highest[p.Item] = Quote{p.Buy, p.City, p.BuySeen, p.BuyPublic}
			}
		}
	}
	return
}

type Material struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
	Price int64  `json:"price"`
	City  string `json:"city"`
}

// CraftQuality is what the finished item sells for at one quality level, so
// you can see the upside from crafting up (the profit is still worked out at
// Normal, since you can't count on a higher quality coming out).
type CraftQuality struct {
	Quality int   `json:"quality"`
	Instant int64 `json:"instant"` // best buy order at this quality
	List    int64 `json:"list"`    // cheapest sell order at this quality
}

// finishedQualities finds, for each item, the best buy order and cheapest
// sell order at every quality level in one pass.
func (b *Book) finishedQualities(maxAge time.Duration) map[string]map[int][2]int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	out := map[string]map[int][2]int64{}
	for _, p := range b.Prices {
		if p.City == "" {
			continue
		}
		m := out[p.Item]
		if m == nil {
			m = map[int][2]int64{}
			out[p.Item] = m
		}
		v := m[p.Quality]
		if p.Buy > 0 && now.Sub(p.BuySeen) <= maxAge && p.Buy > v[0] {
			v[0] = p.Buy
		}
		if p.Sell > 0 && now.Sub(p.SellSeen) <= maxAge && (v[1] == 0 || p.Sell < v[1]) {
			v[1] = p.Sell
		}
		m[p.Quality] = v
	}
	return out
}

type Craft struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Tier       string     `json:"tier"`
	Category   string     `json:"category"`
	Cost       int64      `json:"cost"`       // materials + station fee per item, after returns
	Fee        int64      `json:"fee"`        // station fee per item
	Instant    int64      `json:"instant"`    // best buy order
	InstantAt  string     `json:"instantAt"`  //
	List       int64      `json:"list"`       // cheapest sell order
	ListAt     string     `json:"listAt"`     //
	Profit     int64      `json:"profit"`     // selling instantly, after tax
	ListProfit int64      `json:"listProfit"` // listing a sell order, after tax and fee
	Percent    float64    `json:"percent"`
	AgeMin     int        `json:"ageMin"`
	Public     bool           `json:"public"` // uses at least one public price
	Materials  []Material     `json:"materials"`
	Qualities  []CraftQuality `json:"qualities"` // what it sells for at each quality, if more than Normal seen
}

const setupFee = 0.025

// Crafts works out what every recipe costs with the prices we've seen,
// and what the item sells for. returnRate is the share of materials the
// crafting station gives back (e.g. 0.152 in a royal city without focus).
// stationFee is the silver per 100 nutrition the station owner charges.
func (b *Book) Crafts(tax, returnRate, stationFee float64, maxAge time.Duration) []Craft {
	cheapest, highest := b.bestPrices(maxAge)
	finished := b.finishedQualities(maxAge)
	now := time.Now()
	out := []Craft{}

	for id, r := range recipes {
		if len(r.Materials) == 0 || r.Makes < 1 {
			continue
		}
		sellNow, okNow := highest[id]
		sellList, okList := cheapest[id]
		if !okNow && !okList {
			continue
		}

		oldest := now
		if okNow {
			oldest = sellNow.Seen
		}
		if okList && sellList.Seen.Before(oldest) {
			oldest = sellList.Seen
		}

		var cost float64
		var mats []Material
		complete := true
		anyPublic := (okNow && sellNow.Public) || (okList && sellList.Public)
		for _, m := range r.Materials {
			if len(m) < 3 {
				complete = false
				break
			}
			mid, _ := m[0].(string)
			count, _ := m[1].(float64)
			returnable, _ := m[2].(float64)
			q, ok := cheapest[mid]
			if !ok {
				complete = false
				break
			}
			part := float64(q.Price) * count
			if returnable == 1 {
				part *= 1 - returnRate
			}
			cost += part
			if q.Public {
				anyPublic = true
			}
			if q.Seen.Before(oldest) {
				oldest = q.Seen
			}
			mats = append(mats, Material{mid, itemName(mid), int(count), q.Price, cityName(q.City)})
		}
		if !complete {
			continue
		}

		// The station uses "nutrition": item value x 0.1125 for each item made.
		// The owner charges stationFee silver for every 100 nutrition.
		fee := r.Value * float64(r.Makes) * 0.1125 * stationFee / 100
		per := int64((cost + fee + float64(r.Silver)) / float64(r.Makes))
		c := Craft{
			ID: id, Name: itemName(id), Tier: tier(id), Category: r.Category,
			Public: anyPublic, Cost: per, Fee: int64(fee / float64(r.Makes)), Materials: mats, AgeMin: int(now.Sub(oldest).Minutes()),
			Profit: -1 << 62, ListProfit: -1 << 62,
		}
		if okNow {
			c.Instant, c.InstantAt = sellNow.Price, cityName(sellNow.City)
			c.Profit = int64(float64(sellNow.Price)*(1-tax)) - per
		}
		if okList {
			c.List, c.ListAt = sellList.Price, cityName(sellList.City)
			c.ListProfit = int64(float64(sellList.Price)*(1-tax-setupFee)) - per
		}
		best := c.Profit
		if c.ListProfit > best {
			best = c.ListProfit
		}
		if per > 0 {
			c.Percent = float64(best) / float64(per) * 100
		}
		if fq := finished[id]; fq != nil {
			for q := 1; q <= 5; q++ {
				if v, ok := fq[q]; ok && (v[0] > 0 || v[1] > 0) {
					c.Qualities = append(c.Qualities, CraftQuality{Quality: q, Instant: v[0], List: v[1]})
				}
			}
			if len(c.Qualities) < 2 { // only worth showing when there's more than Normal
				c.Qualities = nil
			}
		}
		out = append(out, c)
	}

	sort.Slice(out, func(i, j int) bool {
		return max(out[i].Profit, out[i].ListProfit) > max(out[j].Profit, out[j].ListProfit)
	})
	for i := range out { // 0 means "no price" in the window
		if out[i].Instant == 0 {
			out[i].Profit = 0
		}
		if out[i].List == 0 {
			out[i].ListProfit = 0
		}
	}
	return out
}
