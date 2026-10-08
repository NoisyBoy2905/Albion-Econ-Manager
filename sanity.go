package main

import (
	"sort"
	"strconv"
	"time"
)

// One listing isn't a market price. Players post joke/troll sell listings
// (999,999,999) and lowball buy orders (1 silver). Every price has to be judged
// against the same item's other prices — buy AND sell, across cities — before
// it's used. This is the one place that filtering lives; every feature (flips,
// crafting, trips, the all-prices tab, the hero and flips.csv) goes through the
// clean snapshot it produces, so they all filter the same way.

const (
	trollMult = 3.0  // a sell listing over this x fair (or x the city's best buy) is a troll
	junkFrac  = 10.0 // a buy order below fair/junkFrac is a lowball
	suspMult  = 3.0  // a buy order over this x fair is kept but flagged "check this"
)

// HiddenPrice is one price the filter dropped, shown behind a toggle in the
// window so you can see what was removed and why.
type HiddenPrice struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Quality int    `json:"quality"`
	City    string `json:"city"`
	Side    string `json:"side"` // "sell" or "buy"
	Price   int64  `json:"price"`
	Reason  string `json:"reason"`
}

func median(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// fairPrice builds a reference price for one item+quality from every city's
// cheapest sell listing and best buy order. It takes a rough median first, drops
// the obvious outliers (so a troll price can't define "fair"), then takes the
// median of what's left with buy orders counted double — silver is actually
// committed to a buy order, while a listing is only a hope. Finally it won't let
// fair rise above the dearest sell that has a real buy within 50% of it.
func fairPrice(sells, buys []int64) int64 {
	all := append(append([]int64(nil), sells...), buys...)
	prelim := median(all)
	if prelim <= 0 {
		return 0
	}
	var kept, keptBuys []int64
	for _, s := range sells {
		if float64(s) <= trollMult*float64(prelim) {
			kept = append(kept, s)
		}
	}
	for _, b := range buys {
		if float64(b) >= float64(prelim)/junkFrac && float64(b) <= suspMult*float64(prelim) {
			kept = append(kept, b, b) // buy orders weigh double
			keptBuys = append(keptBuys, b)
		}
	}
	if len(kept) == 0 {
		kept = all
	}
	f := median(kept)
	if len(keptBuys) > 0 {
		capHi := int64(0)
		for _, b := range keptBuys {
			if b > capHi {
				capHi = b
			}
		}
		for _, s := range sells {
			for _, b := range keptBuys {
				if float64(s) <= 1.5*float64(b) && s > capHi {
					capHi = s
				}
			}
		}
		if capHi > 0 && f > capHi {
			f = capHi
		}
	}
	return f
}

func isTrollSell(price, fair, cityBuy int64) bool {
	if fair > 0 && float64(price) > trollMult*float64(fair) {
		return true
	}
	if cityBuy > 0 && float64(price) > trollMult*float64(cityBuy) {
		return true
	}
	return false
}

func isJunkBuy(price, fair int64) bool {
	return fair > 0 && float64(price) < float64(fair)/junkFrac
}

func isSuspiciousBuy(price, fair int64) bool {
	return fair > 0 && float64(price) > suspMult*float64(fair)
}

func trollReason(price, fair, cityBuy int64) string {
	if fair > 0 && float64(price) > trollMult*float64(fair) {
		return "over 3x fair price"
	}
	return "over 3x the city's best buy"
}

// judgeSell / judgeBuy classify one price for the history log.
func judgeSell(price, fair, cityBuy int64) (bool, string) {
	if isTrollSell(price, fair, cityBuy) {
		return true, trollReason(price, fair, cityBuy)
	}
	return false, ""
}

func judgeBuy(price, fair int64) (bool, string) {
	if isJunkBuy(price, fair) {
		return true, "lowball buy order"
	}
	if isSuspiciousBuy(price, fair) {
		return true, "over 3x fair price"
	}
	return false, ""
}

// fairPrices returns the fair price per item+quality key, from prices fresh on
// at least one side within maxAge. Used to flag the history log.
func fairPrices(prices map[string]*Price, maxAge time.Duration) map[string]int64 {
	now := time.Now()
	sells := map[string][]int64{}
	buys := map[string][]int64{}
	for _, p := range prices {
		if p.City == "" {
			continue
		}
		k := p.Item + "|" + strconv.Itoa(p.Quality)
		if p.Sell > 0 && now.Sub(p.SellSeen) <= maxAge {
			sells[k] = append(sells[k], p.Sell)
		}
		if p.Buy > 0 && now.Sub(p.BuySeen) <= maxAge {
			buys[k] = append(buys[k], p.Buy)
		}
	}
	fair := map[string]int64{}
	for k := range sells {
		fair[k] = fairPrice(sells[k], buys[k])
	}
	for k := range buys {
		if _, ok := fair[k]; !ok {
			fair[k] = fairPrice(nil, buys[k])
		}
	}
	return fair
}

func hiddenOf(p *Price, side string, price int64, reason string) HiddenPrice {
	return HiddenPrice{ID: p.Item, Name: itemName(p.Item), Quality: p.Quality,
		City: cityName(p.City), Side: side, Price: price, Reason: reason}
}

// sanitize builds a cleaned copy of the price book: troll sell levels and junk
// buy orders removed, each price's Sell/Buy recomputed from what's left, and two
// transient flags set — trusted (enough data to use the sell for list flips and
// crafting) and check (the best buy is suspiciously high: keep it, but flag it).
// It also returns the fair price per item+quality and the list of dropped prices.
func sanitize(prices map[string]*Price, maxAge time.Duration) (map[string]*Price, []HiddenPrice, map[string]int64) {
	now := time.Now()

	// Gather per-item+quality sell and buy samples, and count how many prices we
	// have to judge against (one city's lone listing can't be judged).
	sells := map[string][]int64{}
	buys := map[string][]int64{}
	fresh := func(p *Price) (bool, bool) {
		return p.Sell > 0 && now.Sub(p.SellSeen) <= maxAge,
			p.Buy > 0 && now.Sub(p.BuySeen) <= maxAge
	}
	for _, p := range prices {
		if p.City == "" {
			continue
		}
		fs, fb := fresh(p)
		k := p.Item + "|" + strconv.Itoa(p.Quality)
		if fs {
			sells[k] = append(sells[k], p.Sell)
		}
		if fb {
			buys[k] = append(buys[k], p.Buy)
		}
	}
	fair := map[string]int64{}
	samples := map[string]int{}
	for k := range sells {
		fair[k] = fairPrice(sells[k], buys[k])
		samples[k] = len(sells[k]) + len(buys[k])
	}
	for k := range buys {
		if _, ok := fair[k]; !ok {
			fair[k] = fairPrice(nil, buys[k])
			samples[k] = len(buys[k])
		}
	}

	clean := map[string]*Price{}
	var hidden []HiddenPrice
	for _, p := range prices {
		if p.City == "" {
			continue
		}
		fs, fb := fresh(p)
		if !fs && !fb {
			continue
		}
		k := p.Item + "|" + strconv.Itoa(p.Quality)
		f := fair[k]
		cityBuy := int64(0)
		if fb {
			cityBuy = p.Buy
		}
		cp := &Price{Item: p.Item, Quality: p.Quality, City: p.City,
			SellSeen: p.SellSeen, SellPublic: p.SellPublic,
			BuySeen: p.BuySeen, BuyPublic: p.BuyPublic}

		// Sell side: drop troll listings, keep the cheapest real one.
		if fs {
			if len(p.SellLevels) > 0 {
				var keep []Level
				for _, l := range p.SellLevels {
					if isTrollSell(l.Price, f, cityBuy) {
						hidden = append(hidden, hiddenOf(p, "sell", l.Price, trollReason(l.Price, f, cityBuy)))
					} else {
						keep = append(keep, l)
					}
				}
				cp.SellLevels = keep
				if len(keep) > 0 {
					cp.Sell = keep[0].Price
				}
			} else if isTrollSell(p.Sell, f, cityBuy) {
				hidden = append(hidden, hiddenOf(p, "sell", p.Sell, trollReason(p.Sell, f, cityBuy)))
			} else {
				cp.Sell = p.Sell
			}
		}
		// A lone public listing with nothing to judge it against isn't trusted
		// for list flips or crafting. Our own scans are trusted: we saw them
		// live in the market, and a public price is trusted once a second price
		// (sell or buy, any city) corroborates it.
		cp.trusted = !p.SellPublic || samples[k] >= 2

		// Buy side: drop lowball orders, keep the best real one; flag if high.
		if fb {
			if len(p.BuyLevels) > 0 {
				var keep []Level
				for _, l := range p.BuyLevels {
					if isJunkBuy(l.Price, f) {
						hidden = append(hidden, hiddenOf(p, "buy", l.Price, "lowball buy order"))
					} else {
						keep = append(keep, l)
					}
				}
				cp.BuyLevels = keep
				if len(keep) > 0 {
					cp.Buy = keep[0].Price
				}
			} else if isJunkBuy(p.Buy, f) {
				hidden = append(hidden, hiddenOf(p, "buy", p.Buy, "lowball buy order"))
			} else {
				cp.Buy = p.Buy
			}
			if cp.Buy > 0 && isSuspiciousBuy(cp.Buy, f) {
				cp.check = true
			}
		}

		if cp.Sell == 0 && cp.Buy == 0 {
			continue // nothing real left
		}
		clean[key(p.Item, p.Quality, p.City)] = cp
	}
	return clean, hidden, fair
}
