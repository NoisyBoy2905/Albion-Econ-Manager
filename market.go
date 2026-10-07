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
	BuyFor   int64 // cheapest price
	SellFor  int64 // best price
	Profit   int64 // profit on the first one
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

// Flips: buy at the cheapest sell orders in one city, then sell instantly
// to the best buy orders in another city (like the Black Market).
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
			for _, to := range list {
				if to == from || to.City == "" || to.City == from.City || to.Buy == 0 || now.Sub(to.BuySeen) > maxAge {
					continue
				}
				profit := int64(float64(to.Buy)*(1-tax)) - from.Sell
				if profit <= 0 {
					continue
				}
				sells, buys := from.SellLevels, to.BuyLevels
				if len(sells) == 0 { // prices saved by an older version
					sells = []Level{{from.Sell, 1}}
				}
				if len(buys) == 0 {
					buys = []Level{{to.Buy, 1}}
				}
				qty, total, cost := match(sells, buys, tax)
				age := now.Sub(from.SellSeen)
				if a := now.Sub(to.BuySeen); a > age {
					age = a
				}
				out = append(out, Flip{
					Item: from.Item, Quality: from.Quality,
					From: from.City, To: to.City,
					BuyFor: from.Sell, SellFor: to.Buy,
					Profit: profit, Percent: float64(profit) / float64(from.Sell) * 100,
					Qty: qty, Total: total, Cost: cost, Age: age,
					Public: from.SellPublic || to.BuyPublic,
				})
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
