package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Public prices come from the Albion Data Project: a free website that
// collects prices from lots of players' scans. Only used when you press
// the "Get public prices" button.

var regions = map[string]string{
	"europe": "https://europe.albion-online-data.com",
	"west":   "https://west.albion-online-data.com",
	"east":   "https://east.albion-online-data.com",
}

// The API uses city names; we use the game's zone IDs.
var publicCities = map[string]string{
	"Thetford": "0007", "Lymhurst": "1002", "Bridgewatch": "2004", "Caerleon": "3005",
	"Martlock": "3008", "Fort Sterling": "4002", "Brecilien": "5003", "Black Market": "3003",
}

type apiPrice struct {
	Item     string `json:"item_id"`
	City     string `json:"city"`
	Quality  int    `json:"quality"`
	SellMin  int64  `json:"sell_price_min"`
	SellDate string `json:"sell_price_min_date"`
	BuyMax   int64  `json:"buy_price_max"`
	BuyDate  string `json:"buy_price_max_date"`
}

type publicJSON struct {
	Running bool   `json:"running"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Prices  int    `json:"prices"`
	Error   string `json:"error"`
	Last    string `json:"last"` // when the last download finished
}

type Public struct {
	mu    sync.Mutex
	state publicJSON
}

// itemsToFetch is every item with a recipe, every material, and every
// tiered item we know a name for.
func itemsToFetch() []string {
	set := map[string]bool{}
	for id, r := range recipes {
		set[id] = true
		for _, m := range r.Materials {
			if s, ok := m[0].(string); ok {
				set[s] = true
			}
		}
	}
	for id := range itemNames {
		if len(id) > 1 && id[0] == 'T' && id[1] >= '1' && id[1] <= '8' {
			set[id] = true
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// batches splits the items so each web address stays under the length
// limit, like the Albion Analyser site does.
func batches(items []string, maxChars int) [][]string {
	var out [][]string
	var cur []string
	size := 0
	for _, id := range items {
		if size+len(id)+1 > maxChars && len(cur) > 0 {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, id)
		size += len(id) + 1
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func parseAPITime(s string) time.Time {
	t, err := time.Parse("2006-01-02T15:04:05", s)
	if err != nil || t.Year() < 2000 {
		return time.Time{}
	}
	return t // the API's times are UTC
}

// Fetch downloads public prices in the background. wait is the pause
// between requests, to stay under the site's limit (180 a minute,
// 300 every 5 minutes).
func (a *App) fetchPublic(base string, wait time.Duration) {
	p := &a.public
	p.mu.Lock()
	if p.state.Running {
		p.mu.Unlock()
		return
	}
	groups := batches(itemsToFetch(), 1800)
	p.state = publicJSON{Running: true, Total: len(groups), Last: p.state.Last}
	p.mu.Unlock()

	var cities []string
	for name := range publicCities {
		cities = append(cities, name)
	}
	sort.Strings(cities)
	loc := url.QueryEscape(strings.Join(cities, ","))
	client := &http.Client{Timeout: 30 * time.Second}
	added := 0

	for i, group := range groups {
		if i > 0 {
			time.Sleep(wait)
		}
		addr := fmt.Sprintf("%s/api/v2/stats/prices/%s.json?locations=%s",
			base, strings.Join(group, ","), loc)
		var rows []apiPrice
		err := getJSON(client, addr, &rows)
		if err != nil {
			// one retry after a longer pause, in case we hit the limit
			time.Sleep(10 * time.Second)
			err = getJSON(client, addr, &rows)
		}
		if err != nil {
			p.mu.Lock()
			p.state.Running = false
			p.state.Error = "Stopped after " + fmt.Sprint(i) + " of " + fmt.Sprint(len(groups)) + ": " + err.Error()
			p.mu.Unlock()
			a.event("warn", "Public prices stopped: %v", err)
			return
		}
		added += a.book.AddPublic(rows)
		p.mu.Lock()
		p.state.Done = i + 1
		p.state.Prices = added
		p.mu.Unlock()
	}
	a.book.Flush()

	p.mu.Lock()
	p.state.Running = false
	p.state.Last = time.Now().Format("15:04")
	p.mu.Unlock()
	a.event("scan", "Downloaded %s public prices", comma(int64(added)))
	a.saveFlips()
}

func getJSON(c *http.Client, addr string, into any) error {
	req, _ := http.NewRequest("GET", addr, nil)
	req.Header.Set("User-Agent", "albion-market-sniffer (personal tool)")
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("the site answered %s", res.Status)
	}
	return json.NewDecoder(res.Body).Decode(into)
}

// AddPublic merges public prices into the book. Your own prices win
// unless the public one is newer.
func (b *Book) AddPublic(rows []apiPrice) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	added := 0
	for _, r := range rows {
		city, ok := publicCities[r.City]
		if !ok || r.Quality < 1 {
			continue
		}
		k := key(r.Item, r.Quality, city)
		e := b.Prices[k]
		fresh := false
		if r.SellMin > 0 {
			if t := parseAPITime(r.SellDate); !t.IsZero() {
				if e == nil {
					e = &Price{Item: r.Item, Quality: r.Quality, City: city}
				}
				if t.After(e.SellSeen) {
					e.Sell, e.SellSeen, e.SellPublic = r.SellMin, t, true
					e.SellLevels = []Level{{r.SellMin, 1}}
					fresh = true
				}
			}
		}
		if r.BuyMax > 0 {
			if t := parseAPITime(r.BuyDate); !t.IsZero() {
				if e == nil {
					e = &Price{Item: r.Item, Quality: r.Quality, City: city}
				}
				if t.After(e.BuySeen) {
					e.Buy, e.BuySeen, e.BuyPublic = r.BuyMax, t, true
					e.BuyLevels = []Level{{r.BuyMax, 1}}
					fresh = true
				}
			}
		}
		if fresh {
			b.Prices[k] = e
			added++
		}
	}
	return added
}

func (a *App) publicState() publicJSON {
	a.public.mu.Lock()
	defer a.public.mu.Unlock()
	return a.public.state
}
