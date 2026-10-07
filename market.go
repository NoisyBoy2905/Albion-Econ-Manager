package main

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// One order, as the game sends it (it's JSON text inside the packet).
type Order struct {
	ID          int64  `json:"Id"`
	Item        string `json:"ItemTypeId"`
	Location    string `json:"LocationId"`
	Quality     int    `json:"QualityLevel"`
	Enchantment int    `json:"EnchantmentLevel"`
	Price       int64  `json:"UnitPriceSilver"`
	Amount      int    `json:"Amount"`
	Type        string `json:"AuctionType"` // "offer" = sell order, "request" = buy order
}

// Short names for the market zones, used in the flip table.
var marketNames = map[string]string{
	"0007":          "Thetford",
	"1002":          "Lymhurst",
	"2004":          "Bridgewatch",
	"3005":          "Caerleon",
	"3013-Auction2": "Caerleon",
	"3008":          "Martlock",
	"4002":          "Fort Sterling",
	"5003":          "Brecilien",
	"3003":          "Black Market",
}

func cityName(id string) string {
	if n, ok := marketNames[id]; ok {
		return n
	}
	if n, ok := zoneNames[id]; ok {
		return n
	}
	if id == "" {
		return "Unknown"
	}
	return id
}

var placeID = regexp.MustCompile(`^[0-9]{3,6}$`)

// looksLikePlace checks if a string is a market location id like "3005"
func looksLikePlace(s string) bool {
	return placeID.MatchString(s) ||
		strings.HasSuffix(s, "-Auction2") ||
		strings.HasPrefix(s, "BLACKBANK-") ||
		strings.HasSuffix(s, "-HellDen")
}

// Level is one price on the order list and how many are on offer at it.
type Level struct {
	Price  int64
	Amount int
}

// Price holds the orders we've seen for one item, quality and city.
type Price struct {
	Item       string
	Quality    int
	City       string
	Sell       int64   // cheapest sell order: what you pay to buy
	SellLevels []Level // all sell orders, cheapest first
	SellSeen   time.Time
	SellPublic bool    // true if the sell price came from the public website
	Buy        int64   // best buy order: what you get selling instantly
	BuyLevels  []Level // all buy orders, highest first
	BuySeen    time.Time
	BuyPublic  bool
}

type Book struct {
	mu     sync.Mutex
	Prices map[string]*Price
	path   string
}

func NewBook(path string) *Book {
	b := &Book{Prices: map[string]*Price{}, path: path}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &b.Prices)
	}
	return b
}

func key(item string, q int, city string) string {
	return item + "|" + strconv.Itoa(q) + "|" + city
}

func (b *Book) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.Prices)
}

// levels adds up the amounts at each price and sorts them.
func levels(amounts map[int64]int, highestFirst bool) []Level {
	out := make([]Level, 0, len(amounts))
	for p, n := range amounts {
		out = append(out, Level{p, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if highestFirst {
			return out[i].Price > out[j].Price
		}
		return out[i].Price < out[j].Price
	})
	return out
}

// Add takes one page of orders from the market screen. A page is fresh,
// so it replaces whatever we had for those items.
func (b *Book) Add(orders []Order, city string, divide bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()

	type side struct{ sell, buy map[int64]int }
	page := map[string]*side{}
	info := map[string]Order{}

	for _, o := range orders {
		price := o.Price
		if divide {
			price /= 10000
		}
		if price <= 0 {
			continue
		}
		where := city
		if o.Location != "" {
			where = o.Location
		}
		k := key(o.Item, o.Quality, where)
		info[k] = Order{Item: o.Item, Quality: o.Quality, Location: where}
		p := page[k]
		if p == nil {
			p = &side{map[int64]int{}, map[int64]int{}}
			page[k] = p
		}
		amount := o.Amount
		if amount < 1 {
			amount = 1
		}
		if o.Type == "request" {
			p.buy[price] += amount
		} else {
			p.sell[price] += amount
		}
	}

	for k, p := range page {
		e := b.Prices[k]
		if e == nil {
			o := info[k]
			e = &Price{Item: o.Item, Quality: o.Quality, City: o.Location}
			b.Prices[k] = e
		}
		if len(p.sell) > 0 {
			e.SellLevels = levels(p.sell, false)
			e.SellPublic = false
			e.Sell, e.SellSeen = e.SellLevels[0].Price, now
		}
		if len(p.buy) > 0 {
			e.BuyLevels = levels(p.buy, true)
			e.BuyPublic = false
			e.Buy, e.BuySeen = e.BuyLevels[0].Price, now
		}
	}
	b.save()
}

func (b *Book) save() {
	data, err := json.Marshal(b.Prices)
	if err == nil {
		os.WriteFile(b.path, data, 0644)
	}
}

type Flip struct {
	Item     string
	Quality  int
	From, To string
	BuyFor   int64  // cheapest price
	SellFor  int64  // best price
	Mode     string // "instant" = sell into buy orders, "list" = undercut sell orders
	Profit   int64  // profit on the first one
	Percent  float64
	Qty      int   // how many you can flip before it stops being worth it
	Total    int64 // profit on all of them
	Cost     int64 // silver needed to buy all of them
	Public   bool  // uses a public price, so the quantity isn't known
	Age      time.Duration
}

// match walks both order lists: buy the cheapest sell orders and sell them
// into the best buy orders, for as long as each one still makes a profit.
func match(sells, buys []Level, tax float64) (qty int, total, cost int64) {
	sells = append([]Level(nil), sells...)
	buys = append([]Level(nil), buys...)
	i, j := 0, 0
	for i < len(sells) && j < len(buys) {
		net := int64(float64(buys[j].Price)*(1-tax)) - sells[i].Price
		if net <= 0 {
			break
		}
		take := sells[i].Amount
		if buys[j].Amount < take {
			take = buys[j].Amount
		}
		qty += take
		total += int64(take) * net
		cost += int64(take) * sells[i].Price
		sells[i].Amount -= take
		buys[j].Amount -= take
		if sells[i].Amount == 0 {
			i++
		}
		if buys[j].Amount == 0 {
			j++
		}
	}
	return qty, total, cost
}

// listFill is the "list a sell order" version of match. You buy the cheapest
// sell orders in one city and, in another, list them yourself at the going
// rate (proceeds, already after tax and the listing fee). You keep buying for
// as long as each one still costs less than you'd net. There's no limit from
// the other side, so this is an upper bound: listing a lot undercuts yourself.
func listFill(sells []Level, proceeds int64) (qty int, total, cost int64) {
	for _, l := range sells {
		net := proceeds - l.Price
		if net <= 0 {
			break
		}
		qty += l.Amount
		total += int64(l.Amount) * net
		cost += int64(l.Amount) * l.Price
	}
	return qty, total, cost
}

// Flips: buy at the cheapest sell orders in one city, then in another city
// either sell instantly to the best buy orders (like the Black Market) or
// list your own sell order at the going rate. Each flip keeps whichever of
// the two earns more in total.
func (b *Book) Flips(tax float64, maxAge time.Duration) []Flip {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()

	byItem := map[string][]*Price{}
	for _, p := range b.Prices {
		k := p.Item + "|" + strconv.Itoa(p.Quality)
		byItem[k] = append(byItem[k], p)
	}

	var out []Flip
	for _, list := range byItem {
		for _, from := range list {
			if from.City == "" || from.Sell == 0 || now.Sub(from.SellSeen) > maxAge {
				continue
			}
			sells := from.SellLevels
			if len(sells) == 0 { // prices saved by an older version
				sells = []Level{{from.Sell, 1}}
			}
			for _, to := range list {
				if to == from || to.City == "" || to.City == from.City {
					continue
				}

				// Sell instantly into the destination's buy orders.
				var inProfit, inTotal, inCost int64
				var inQty int
				if to.Buy > 0 && now.Sub(to.BuySeen) <= maxAge {
					if p := int64(float64(to.Buy)*(1-tax)) - from.Sell; p > 0 {
						buys := to.BuyLevels
						if len(buys) == 0 {
							buys = []Level{{to.Buy, 1}}
						}
						inProfit = p
						inQty, inTotal, inCost = match(sells, buys, tax)
					}
				}

				// List your own sell order, undercutting the cheapest one there.
				var liProfit, liTotal, liCost int64
				var liQty int
				if to.Sell > 0 && now.Sub(to.SellSeen) <= maxAge {
					proceeds := int64(float64(to.Sell) * (1 - tax - setupFee))
					if p := proceeds - from.Sell; p > 0 {
						liProfit = p
						liQty, liTotal, liCost = listFill(sells, proceeds)
					}
				}

				if inProfit <= 0 && liProfit <= 0 {
					continue
				}

				// Keep whichever strategy earns more in total.
				f := Flip{
					Item: from.Item, Quality: from.Quality,
					From: from.City, To: to.City, BuyFor: from.Sell,
					Mode: "instant", SellFor: to.Buy,
					Profit: inProfit, Qty: inQty, Total: inTotal, Cost: inCost,
					Public: from.SellPublic || to.BuyPublic,
				}
				sellSeen := to.BuySeen
				if liTotal > inTotal {
					f.Mode, f.SellFor = "list", to.Sell
					f.Profit, f.Qty, f.Total, f.Cost = liProfit, liQty, liTotal, liCost
					f.Public = from.SellPublic || to.SellPublic
					sellSeen = to.SellSeen
				}
				f.Percent = float64(f.Profit) / float64(from.Sell) * 100
				f.Age = now.Sub(from.SellSeen)
				if a := now.Sub(sellSeen); a > f.Age {
					f.Age = a
				}
				out = append(out, f)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out
}

// parseOrders looks for a list of JSON strings that look like market orders.
// Albion sends them as a string array inside a response.
func parseOrders(v any) []Order {
	list, ok := v.([]string)
	if !ok || len(list) == 0 || !strings.Contains(list[0], "UnitPriceSilver") {
		return nil
	}
	var orders []Order
	for _, s := range list {
		var o Order
		if json.Unmarshal([]byte(s), &o) == nil && o.Item != "" {
			orders = append(orders, o)
		}
	}
	return orders
}
