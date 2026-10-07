//go:build !windows

package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

// On other systems there's no capture, but -demo shows the window
// with made-up orders, which is handy for working on the design.
func main() {
	demo := flag.Bool("demo", false, "show the window with made-up prices")
	replay := flag.String("replay", "", "print every message in a recording")
	find := flag.Float64("find", 0, "with -replay: only show messages containing this number")
	flag.Parse()
	if runExportHistoryFlag() {
		return
	}
	if *replay != "" && *find != 0 {
		if err := findInRecording(*replay, *find); err != nil {
			fmt.Println(err)
		}
		return
	}
	if *replay != "" {
		loadItemNames()
		loadRecipes()
		app := NewApp(os.DevNull)
		if err := dumpRecording(*replay, app); err != nil {
			fmt.Println(err)
		}
		return
	}
	if !*demo {
		fmt.Println("This sniffer is built for Windows. Run the .exe on your PC.")
		return
	}
	loadItemNames()
	loadRecipes()
	os.Remove("demo-prices.json")
	os.Remove("demo-history.jsonl")
	app := NewApp("demo-prices.json")
	h := NewHistory("demo-history.jsonl")
	app.hist, app.book.hist = h, h
	flushOnExit(app)
	app.adapters = 2
	addr, err := app.serve(*port)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("Demo window:", addr)

	o := func(item string, q int, price int64, kind string, amount int) Order {
		return Order{Item: item, Quality: q, Price: price * 10000, Type: kind, Amount: amount}
	}
	app.packet()
	app.handle(Message{Kind: msgResponse, Params: map[byte]any{8: "1000"}})
	app.handle(Message{Kind: msgResponse, Params: map[byte]any{8: "1002"}})
	app.book.Add([]Order{
		o("T4_BAG", 1, 2100, "offer", 4), o("T4_BAG", 1, 2600, "offer", 10),
		o("T6_2H_HOLYSTAFF", 2, 98500, "offer", 1), o("T6_2H_HOLYSTAFF", 2, 104000, "offer", 2),
		o("T5_MAIN_SWORD@1", 1, 41200, "offer", 3), o("T4_MOUNT_HORSE", 1, 24000, "offer", 6),
		o("T7_ARMOR_PLATE_SET1", 3, 310000, "offer", 1),
		o("T6_PLANKS", 1, 2400, "offer", 500), o("T6_CLOTH", 1, 2600, "offer", 300),
		o("T5_PLANKS", 1, 900, "offer", 800), o("T5_CLOTH", 1, 1100, "offer", 400),
		o("T4_PLANKS", 1, 300, "offer", 900), o("T4_CLOTH", 1, 380, "offer", 700),
		// Dual Swords for sale, plus the bars and leather to craft them.
		o("T5_2H_DUALSWORD", 1, 48000, "offer", 2),
		o("T5_METALBAR", 1, 1100, "offer", 800), o("T5_LEATHER", 1, 1100, "offer", 500),
	}, "1002", true)
	if os.Getenv("DEMO_ONE_CITY") != "" { // like a first visit: one market, no flips yet
		for {
			time.Sleep(time.Second)
			app.packet()
		}
	}
	app.handle(Message{Kind: msgResponse, Params: map[byte]any{8: "3003"}})
	app.book.Add([]Order{
		o("T4_BAG", 1, 3000, "request", 8), o("T6_2H_HOLYSTAFF", 1, 131000, "request", 2),
		o("T6_2H_HOLYSTAFF", 2, 131000, "request", 2),
		o("T5_MAIN_SWORD@1", 1, 47800, "request", 5), o("T7_ARMOR_PLATE_SET1", 3, 402500, "request", 1),
		o("T5_2H_HOLYSTAFF", 1, 38500, "request", 4), o("T4_2H_HOLYSTAFF", 1, 9800, "request", 9),
		o("T5_2H_DUALSWORD", 1, 55000, "request", 3), // buy cheap in Lymhurst, sell here
	}, "3003", true)
	app.handle(Message{Kind: msgResponse, Params: map[byte]any{8: "3005"}})
	app.book.Add([]Order{o("T4_MOUNT_HORSE", 1, 27900, "request", 3), o("T5_2H_HOLYSTAFF", 1, 45900, "offer", 2),
		o("T4_BAG", 1, 4000, "offer", 6)}, "3005", true) // bags list high here: a "list" flip from Lymhurst
	app.event("scan", "7 orders in Black Market, e.g. Master's Great Holy Staff at 131,000 silver")
	app.book.AddPublic([]apiPrice{
		{Item: "T5_BAG", City: "Martlock", Quality: 1, SellMin: 7100, SellDate: time.Now().UTC().Add(-40 * time.Minute).Format("2006-01-02T15:04:05")},
		{Item: "T5_BAG", City: "Black Market", Quality: 1, BuyMax: 9400, BuyDate: time.Now().UTC().Add(-25 * time.Minute).Format("2006-01-02T15:04:05")},
	})

	go func() {
		time.Sleep(8 * time.Second)
		app.book.Add([]Order{o("T8_BAG", 1, 61000, "offer", 3)}, "1002", true)
		app.book.Add([]Order{o("T8_BAG", 1, 88000, "request", 3)}, "3003", true)
		app.event("scan", "1 order in Black Market, e.g. Elder's Bag at 88,000 silver")
	}()

	for {
		time.Sleep(time.Second)
		app.packet()
	}
}
