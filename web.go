package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The window is a web page built into the exe. The exe runs a tiny web
// server that only your own PC can reach, and opens it in your browser.

//go:embed web/index.html web/items.txt web/recipes.json web/icon.png
var webFiles embed.FS

var itemNames = map[string]string{}
var itemWeights = map[string]float64{}

func loadItemNames() {
	f, err := webFiles.Open("web/items.txt")
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		parts := strings.Split(s.Text(), "\t")
		if len(parts) < 2 {
			continue
		}
		itemNames[parts[0]] = parts[1]
		if len(parts) > 2 {
			if kg, err := strconv.ParseFloat(parts[2], 64); err == nil {
				itemWeights[parts[0]] = kg
			}
		}
	}
}

// itemName turns "T4_BAG@1" into "Adept's Bag"
func itemName(id string) string {
	if n, ok := itemNames[id]; ok {
		return n
	}
	base, _, _ := strings.Cut(id, "@")
	if n, ok := itemNames[base]; ok {
		return n
	}
	return id
}

// itemWeight is how heavy one item is in kg (0 if we don't know)
func itemWeight(id string) float64 {
	if kg, ok := itemWeights[id]; ok {
		return kg
	}
	base, _, _ := strings.Cut(id, "@")
	return itemWeights[base]
}

// tier turns "T4_BAG@1" into "4.1"
func tier(id string) string {
	if len(id) < 2 || id[0] != 'T' || id[1] < '1' || id[1] > '8' {
		return ""
	}
	t := string(id[1])
	if _, ench, ok := strings.Cut(id, "@"); ok {
		return t + "." + ench
	}
	return t + ".0"
}

type flipJSON struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Tier    string  `json:"tier"`
	Quality int     `json:"quality"`
	From    string  `json:"from"`
	To      string  `json:"to"`
	Buy     int64   `json:"buy"`
	Sell    int64   `json:"sell"`
	Profit  int64   `json:"profit"`
	Percent float64 `json:"percent"`
	Qty     int     `json:"qty"`
	Total   int64   `json:"total"`
	Cost    int64   `json:"cost"`   // silver to buy all of them
	Weight  float64 `json:"weight"` // kg for one
	Public  bool    `json:"public"`
	AgeMin  int     `json:"ageMin"`
}

type stateJSON struct {
	Listening  bool          `json:"listening"`
	Adapters   int           `json:"adapters"`
	Zone       string        `json:"zone"`
	ZoneName   string        `json:"zoneName"`
	InMarket   bool          `json:"inMarket"`
	Packets    int           `json:"packets"`
	Orders     int           `json:"orders"`
	Prices     int           `json:"prices"`
	Encrypted  int           `json:"encrypted"`
	LastPacket int           `json:"lastPacketSec"` // seconds ago, -1 if never
	Flips      []flipJSON    `json:"flips"`
	Crafts     []Craft       `json:"crafts"`
	All        []PriceRow    `json:"all"`
	Recording  recordingJSON `json:"recording"`
	Public     publicJSON    `json:"public"`
	Events     []Event       `json:"events"`
}

func (a *App) state(tax, returnRate, stationFee float64, maxAge time.Duration) stateJSON {
	flips := a.book.Flips(tax, maxAge)
	out := make([]flipJSON, 0, len(flips))
	for i, f := range flips {
		if i == 300 {
			break
		}
		out = append(out, flipJSON{
			ID: f.Item, Name: itemName(f.Item), Tier: tier(f.Item), Quality: f.Quality,
			From: cityName(f.From), To: cityName(f.To),
			Buy: f.BuyFor, Sell: f.SellFor, Profit: f.Profit, Percent: f.Percent,
			Qty: f.Qty, Total: f.Total, Cost: f.Cost, Weight: itemWeight(f.Item), Public: f.Public,
			AgeMin: int(f.Age.Minutes()),
		})
	}
	crafts := a.book.Crafts(tax, returnRate, stationFee, maxAge)
	if len(crafts) > 300 {
		crafts = crafts[:300]
	}
	prices := a.book.Count()
	all := a.book.All(maxAge, 2000)
	rec := a.recordingState()
	pub := a.publicState()

	a.mu.Lock()
	defer a.mu.Unlock()
	_, inMarket := marketNames[a.city]
	last := -1
	if !a.lastPacket.IsZero() {
		last = int(time.Since(a.lastPacket).Seconds())
	}
	events := make([]Event, len(a.events))
	copy(events, a.events)
	return stateJSON{
		Listening: a.adapters > 0, Adapters: a.adapters,
		Zone: a.city, ZoneName: cityName(a.city), InMarket: inMarket,
		Packets: a.packets, Orders: a.orders, Prices: prices, Encrypted: a.encrypted,
		LastPacket: last, Flips: out, Crafts: crafts, All: all, Events: events, Recording: rec, Public: pub,
	}
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		page, _ := webFiles.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		icon, _ := webFiles.ReadFile("web/icon.png")
		w.Header().Set("Content-Type", "image/png")
		w.Write(icon)
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		tax, err := strconv.ParseFloat(q.Get("tax"), 64)
		if err != nil || tax < 0 || tax > 0.5 {
			tax = 0.04
		}
		mins, err := strconv.Atoi(q.Get("age"))
		if err != nil || mins <= 0 {
			mins = 360
		}
		rr, err := strconv.ParseFloat(q.Get("rr"), 64)
		if err != nil || rr < 0 || rr > 0.9 {
			rr = 0.152
		}
		fee, err := strconv.ParseFloat(q.Get("fee"), 64)
		if err != nil || fee < 0 || fee > 100000 {
			fee = 0
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a.state(tax, rr, fee, time.Duration(mins)*time.Minute))
	})

	mux.HandleFunc("/api/record/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		secs, err := strconv.Atoi(r.URL.Query().Get("seconds"))
		if err != nil || secs < 10 || secs > 600 {
			secs = 120
		}
		if err := a.startRecording(secs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.event("zone", "Recording game traffic for %d seconds", secs)
	})
	mux.HandleFunc("/api/public/fetch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		base, ok := regions[r.URL.Query().Get("region")]
		if !ok {
			base = regions["europe"]
		}
		a.event("zone", "Downloading public prices from the Albion Data Project")
		go a.fetchPublic(base, 1100*time.Millisecond)
	})
	mux.HandleFunc("/api/record/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		a.stopRecording(nil)
	})

	// Only answer requests addressed to this PC, so other websites can't
	// read your prices by pretending to be localhost.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// Buttons can only be pressed from this window, not from other websites.
		if o := r.Header.Get("Origin"); r.Method == http.MethodPost && o != "" &&
			!strings.HasPrefix(o, "http://127.0.0.1:") && !strings.HasPrefix(o, "http://localhost:") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// serve starts the window's web server and returns its address.
func (a *App) serve(port int) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0") // that port's taken, pick any free one
		if err != nil {
			return "", err
		}
	}
	go http.Serve(ln, a.routes())
	return "http://" + ln.Addr().String(), nil
}
