package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const gamePort = 5056

var (
	port  = flag.Int("port", 7356, "port for the window (only your own PC can open it)")
	raw   = flag.Bool("raw-prices", false, "don't divide prices by 10000")
	debug = flag.Bool("debug", false, "print every message the game sends")
)

// Event is one line in the activity list in the window.
type Event struct {
	Time time.Time `json:"time"`
	Kind string    `json:"kind"` // "zone", "scan", "warn"
	Text string    `json:"text"`
}

type App struct {
	book       *Book
	mu         sync.Mutex
	city       string
	packets    int
	orders     int
	encrypted  int
	lastPacket time.Time
	started    time.Time
	adapters   int
	events     []Event
	rec        *Recorder
	lastRec    recordingJSON
	public     Public
	hist       *History // price history log, nil if not recording
	csvTax     float64       // tax and age from the last window poll, so the
	csvAge     time.Duration // flips.csv matches what you're looking at
}

func NewApp(pricesFile string) *App {
	// Defaults until the window first polls: premium tax, 6 hours.
	return &App{book: NewBook(pricesFile), started: time.Now(), csvTax: 0.04, csvAge: 6 * time.Hour}
}

// event saves a line for the window and prints it in the console too.
func (a *App) event(kind, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	fmt.Println(text)
	a.mu.Lock()
	a.events = append(a.events, Event{Time: time.Now(), Kind: kind, Text: text})
	if len(a.events) > 80 {
		a.events = a.events[len(a.events)-80:]
	}
	a.mu.Unlock()
}

func (a *App) packet() {
	a.mu.Lock()
	a.packets++
	a.lastPacket = time.Now()
	a.mu.Unlock()
}

func (a *App) onEncrypted() {
	a.mu.Lock()
	a.encrypted++
	n := a.encrypted
	a.mu.Unlock()
	if n == 1 || n%200 == 0 {
		a.event("warn", "The game is sending encrypted data. Prices can't be read while it does.")
	}
}

func (a *App) handle(m Message) {
	if *debug {
		fmt.Printf("[debug] kind=%d code=%d params=%v\n", m.Kind, m.Code, keys(m.Params))
	}
	if m.Kind != msgResponse {
		return
	}

	// When you change zone, the game tells you where you are.
	if s, ok := m.Params[8].(string); ok && looksLikePlace(s) {
		a.mu.Lock()
		changed := a.city != s
		a.city = s
		a.mu.Unlock()
		if changed {
			if s == "3003" {
				a.event("zone", "Entered the Black Market")
			} else if _, ok := marketNames[s]; ok {
				a.event("zone", "Entered %s market", cityName(s))
			} else {
				a.event("zone", "Entered %s. Go into the marketplace to read prices.", cityName(s))
			}
		}
	}

	orders := parseOrders(m.Extra)
	if orders == nil {
		for _, v := range m.Params {
			if orders = parseOrders(v); orders != nil {
				break
			}
		}
	}
	if orders == nil {
		return
	}

	a.mu.Lock()
	city := a.city
	a.orders += len(orders)
	a.mu.Unlock()

	// Without a city the prices can't be used for flips, so don't save them.
	if city == "" {
		a.event("warn", "Read %d orders but don't know which city you're in yet. Leave the marketplace and go back in.", len(orders))
		return
	}

	a.book.Add(orders, city, !*raw)
	first := orders[0]
	p := first.Price
	if !*raw {
		p /= 10000
	}
	a.event("scan", "%d orders in %s, e.g. %s at %s silver",
		len(orders), cityName(city), itemName(first.Item), comma(p))
	a.saveFlips()
}

func (a *App) saveFlips() {
	file, err := os.Create("flips.csv")
	if err != nil {
		return
	}
	defer file.Close()
	w := csv.NewWriter(file)
	a.mu.Lock()
	tax, age := a.csvTax, a.csvAge
	a.mu.Unlock()
	w.Write([]string{"item", "quality", "buy_in", "sell_in", "sell_mode", "buy_price", "sell_price", "profit_each", "percent", "quantity", "total_profit", "age_minutes"})
	for _, f := range a.book.Flips(tax, age, false, 0, 0) {
		w.Write([]string{
			itemName(f.Item), strconv.Itoa(f.Quality), cityName(f.From), cityName(f.To), f.Mode,
			strconv.FormatInt(f.BuyFor, 10), strconv.FormatInt(f.SellFor, 10),
			strconv.FormatInt(f.Profit, 10), fmt.Sprintf("%.1f", f.Percent),
			strconv.Itoa(f.Qty), strconv.FormatInt(f.Total, 10),
			strconv.Itoa(int(f.Age.Minutes())),
		})
	}
	w.Flush()
}

func keys(m map[byte]any) []byte {
	var out []byte
	for k := range m {
		out = append(out, k)
	}
	return out
}

func comma(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}
