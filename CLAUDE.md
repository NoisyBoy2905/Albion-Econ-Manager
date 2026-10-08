# CLAUDE.md — working rules for this project

Albion Market Sniffer: reads your own game traffic to find cross-city flips,
crafting profits and trip plans, shown in a small local web window. Go, with an
embedded HTML page. Keep comments short and plain, like the ones already here.

## How to work

1. **Stay in scope.** Do only what the task asks. A past session was asked just
   to add price history ("don't change how flips work") and changed the flip
   maths anyway — that caused a real bug. If something else looks worth doing,
   note it at the end instead of doing it.
2. **Every quantity needs a limit from both sides.** A trade needs a seller
   *and* a buyer. Never work out "how many" from one side of the market only.
   If the other side's limit isn't known, say so and use a cautious cap.
3. **An upper bound must never be used for ranking.** If a number is a best
   case (e.g. "you could list this many"), don't sort or compare on it as if it
   were real, and label it in the window as an estimate.
4. **Check the game's rules before modelling a trade** (see below). If a
   feature depends on a rule that isn't listed here, ask rather than guess.
5. **Write a realistic test first.** For any money maths, write a small, real
   example as a test (like the Martlock flip in `sniffer_test.go`) and sanity
   check the result: would a real player actually make this much?
6. **Run every check, every time, before saying "done":**
   - `gofmt -w .`
   - `go vet ./...` and `GOOS=windows go vet ./...` (the capture code is
     Windows-only, behind build tags)
   - `go test ./...`

## Game rules the code relies on

- **Black Market (zone `3003`)**: only *buys* from players. There are no sell
  orders to buy from, and you can't list your own sell order there. So it is
  never a place you buy from (a flip/trip/craft source) and never a place you
  list into — only a place you sell instantly, into its buy orders. The
  constant `blackMarket` marks it.
- **Royal city markets are their own zones**, e.g. `1002` Lymhurst, `2004`
  Bridgewatch, `3008` Martlock, `4002` Fort Sterling, `0007` Thetford, `5003`
  Brecilien, `3005`/`3013-Auction2` Caerleon. See `marketNames` in `market.go`.
- **Sales tax**: 4% with Premium, 8% without. **Listing fee**: an extra 2.5%
  when you place your own sell order (`setupFee`).
- **Public prices** (Albion Data Project) don't include order amounts, so a
  quantity built on them is capped at 1.

## Layout

- `market.go` — the price book, flip matching (`Flips`, `match`, `listFill`).
- `crafting.go` — recipe costing and craft profit (`Crafts`, `bestPrices`).
- `trips.go` — the trip planner (greedy pack by profit per kg).
- `public.go` — fetching and merging Albion Data Project prices.
- `history.go` — the append-only price log (`history.jsonl`).
- `photon.go` / `protocol18.go` — the Photon packet decoder.
- `web.go` + `web/index.html` — the local window and its JSON API.
- `capture_windows.go` — live capture (Npcap); `capture_other.go` — `-demo`.
