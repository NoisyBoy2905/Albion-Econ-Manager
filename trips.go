package main

import (
	"math"
	"sort"
)

// A trip is a full load for one city-to-city route: what to buy, how many,
// and the profit when you sell it all at the other end.
type Trip struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Picks  []Pick  `json:"picks"`
	Profit int64   `json:"profit"`
	Spent  int64   `json:"spent"`
	Kg     float64 `json:"kg"` // weight actually loaded
}

// A pick is one item in a trip: how many to buy, what it costs and earns.
type Pick struct {
	Name    string  `json:"name"`
	Tier    string  `json:"tier"`
	Quality int     `json:"quality"`
	Public  bool    `json:"public"`
	Weight  float64 `json:"weight"`
	N       int     `json:"n"`
	Spend   int64   `json:"spend"`
	Gain    int64   `json:"gain"`
}

// planTrips packs each city-to-city route with the flips that make the most
// profit per kg, until the carry weight or the silver budget runs out. It's a
// greedy fill: quick, and nearly always the best load or close to it. Items
// with an unknown weight count as weightless and so are packed first.
func planTrips(flips []flipJSON, carryKg float64, budget int64) []Trip {
	routes := map[string][]flipJSON{}
	var order []string // keep the routes in the order flips arrive
	for _, f := range flips {
		k := f.From + "|" + f.To
		if _, ok := routes[k]; !ok {
			order = append(order, k)
		}
		routes[k] = append(routes[k], f)
	}

	perKg := func(f flipJSON) float64 {
		if f.Weight > 0 {
			return float64(f.Total) / float64(f.Qty) / f.Weight
		}
		return math.Inf(1)
	}

	trips := []Trip{}
	for _, k := range order {
		list := append([]flipJSON(nil), routes[k]...)
		sort.SliceStable(list, func(i, j int) bool { return perKg(list[i]) > perKg(list[j]) })

		kg, silver := carryKg, float64(budget)
		var profit, spent int64
		var used float64
		picks := []Pick{}
		for _, f := range list {
			each := float64(f.Cost) / float64(f.Qty)
			n := f.Qty
			if f.Weight > 0 {
				if m := int(math.Floor(kg / f.Weight)); m < n {
					n = m
				}
			}
			if each > 0 {
				if m := int(math.Floor(silver / each)); m < n {
					n = m
				}
			}
			if n <= 0 {
				continue
			}
			gain := int64(math.Round(float64(f.Total) / float64(f.Qty) * float64(n)))
			cost := int64(math.Round(each * float64(n)))
			picks = append(picks, Pick{
				Name: f.Name, Tier: f.Tier, Quality: f.Quality, Public: f.Public,
				Weight: f.Weight, N: n, Spend: cost, Gain: gain,
			})
			kg -= float64(n) * f.Weight
			silver -= float64(cost)
			profit += gain
			spent += cost
			used += float64(n) * f.Weight
		}
		if profit > 0 {
			trips = append(trips, Trip{From: list[0].From, To: list[0].To, Picks: picks, Profit: profit, Spent: spent, Kg: used})
		}
	}
	sort.SliceStable(trips, func(i, j int) bool { return trips[i].Profit > trips[j].Profit })
	return trips
}
