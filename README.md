# Albion Market Sniffer

Reads Albion Online market prices straight from your own game while you browse the market, then finds items you can buy cheap in one city and sell for more in another.

It only **reads** your own network traffic. It never sends anything to the game, never changes the game, and never plays for you. The only time it goes online is when you press **Get public prices**, which downloads prices from the Albion Data Project (it sends a list of item names, nothing about you). The Albion Data Project says Sandbox Interactive has stated that reading and viewing data this way is allowed. Check the [Data Project FAQ](https://www.albion-online-data.com/client-faq) if you're unsure.

## How to run it

1. Install **Npcap** from [npcap.com](https://npcap.com). Tick **"WinPcap API-compatible mode"**. Leave "Restrict to Administrators only" unticked.
2. Double-click `albion-sniffer.exe`. A small console opens (keep it open, it's the engine), and the window opens in your browser.
3. Start Albion and walk into a marketplace. The top of the window shows where you are.
4. Open the market and browse items. Each page you load shows in **Activity** on the right.
5. Go to another city's marketplace (or the Black Market) and look at the same items.
6. Flips appear in the table, with the best one shown big at the top.
7. For crafting, browse the materials (planks, cloth, bars, leather and so on) and some finished items, then open the **Crafting** tab.

In the window you can:
- see **how many** of each flip you can buy and still make a profit, and the **total profit**
- turn on **alerts**: a sound and a Windows notification when a new flip makes more than the amount you choose
- check **crafting** profits, with a return rate setting (royal city or bonus city, with or without focus) and the **station fee**. Click a craft to see its materials and where to buy them
- **record game traffic** to a file (see below)
- plan a **trip**: enter how much you can carry and how much silver you have, and it picks what to buy and where to sell for the most profit
- search items or cities
- switch Premium on or off (4% or 8% tax)
- choose how old prices can be
- turn on **Prefer fresh prices** to rank flips built on older prices lower (each flip shows how sure it is, based on how recently the prices were seen)
- set a **haul cost** (silver per kg) and a **black-zone risk %** to see flips after the cost of carrying goods and the expected loss to ganks on Caerleon and Black Market routes; flips that stop being worth it then drop out (both are 0 by default, so they change nothing until you set them)
- set a minimum total profit
- click any column heading to sort by it

Close the console to stop it. It saves `prices.json` (every price it's seen, so they're still there next time; prices not seen for 30 days are dropped so the file doesn't grow forever) and `flips.csv` (open it in Excel). Saving is batched, and it saves once more when you close it.

Options (run it from a terminal):

| Option | What it does |
|---|---|
| `-port 7356` | Which port the window uses. If it's taken, it picks a free one |
| `-debug` | Print every message the game sends (for fixing things) |
| `-raw-prices` | Don't divide prices by 10,000 (see below) |

## How it works

```
Albion  →  your network card  →  Npcap copies the packets
        →  capture: keep only UDP port 5056 (the game's port)
        →  photon.go: unpack Photon packets into messages
        →  protocol18.go: decode the values inside each message
        →  app.go: spot market orders and your current city
        →  market.go: keep the best prices and work out flips
        →  web.go: send it all to the window every 2 seconds
```

**1. Capturing (capture_windows.go)**
Albion talks to its servers over UDP on port 5056. Npcap lets a program see a copy of every packet your PC receives. We tell it to keep only port 5056, so we ignore everything else (your browser, Discord and so on).

**2. Unpacking Photon (photon.go)**
Albion uses a network library called Photon. Each packet has a 12-byte header, then one or more "commands". Each command carries a message:
- a **request** (your game asking for something, like "show me T4 bags")
- a **response** (the server's answer, like the list of orders)
- an **event** (something happening, like a player moving)

Big messages, like a full page of market orders, don't fit in one packet. They're split into **fragments**, so the code collects the pieces and glues them back together once they've all arrived. Sometimes they arrive in the wrong order, which is why each piece says where it goes.

**3. Decoding values (protocol18.go)**
Inside a message, every value starts with a **type code** (7 = text, 9 = number, and so on) followed by the value. Numbers use a trick called **varints**: small numbers take 1 byte and big ones take more. We have to understand every type, even ones we don't use. If we misread one, we lose our place and everything after it is wrong.

**4. Finding orders (app.go)**
When you open a market page, the server replies with a list of text, one entry per order, written in JSON:

```json
{"ItemTypeId":"T4_BAG","QualityLevel":1,"UnitPriceSilver":21000000,"Amount":1,"AuctionType":"offer"}
```

- `offer` = a sell order (what you'd **pay** to buy it)
- `request` = a buy order (what you'd **get** if you sell instantly)

Instead of trusting the game's message numbers, which change after some updates, the sniffer looks for any list that contains `UnitPriceSilver`. That makes it harder to break.

The orders don't say which city they're in. So when you change zone, the sniffer reads your location from the game (for example `1000` = Lymhurst, `1002` = Lymhurst Market, `3003` = Black Market). It labels the orders with that. City markets are their own zones, so you have to go inside the marketplace building.

**5. Flips (market.go)**
For each item and quality, it buys at the cheapest sell order in one city, then in another city it works out the better of two ways to sell:

```
sell instantly  = best buy order × (1 − tax) − sell order price
list a sell order = cheapest sell order × (1 − tax − 2.5% listing fee) − sell order price
```

Selling instantly dumps into someone else's buy order (handy for the Black Market). Listing a sell order means undercutting the cheapest listing there and waiting for a buyer, which usually pays more but isn't instant. Each flip keeps whichever earns more in total, and the window labels it "sell now" or "list here". The listing quantity is an upper bound, since listing a lot undercuts your own price.

It skips prices older than `-max-age`, because old prices are often already gone.

**6. How many to flip (market.go)**
The market sends a page of orders, each with a price and an amount. The sniffer keeps the whole list, not just the best price. Then it works through both lists like a real trade: buy the cheapest sell order, sell it to the best buy order, and repeat until the next one would lose money. That gives how many to buy and the total profit.

**7. Crafting (crafting.go)**
The game's own data has every recipe, for example 20 planks + 12 cloth for a Great Holy Staff. For each recipe the sniffer:
1. finds the cheapest price seen for each material in any city
2. takes off the **return rate**, the share of materials the crafting station gives back (artefacts never come back)
3. compares that cost with what the item sells for: instantly to a buy order, or by listing a sell order (minus the 2.5% listing fee)

4. adds the **station fee**. Stations charge in "nutrition": each item uses its item value × 0.1125 nutrition, and the owner charges a set amount of silver per 100 nutrition (you type this in from the station). Item values come from the game data: for crafted items it's the total value of their materials.

It uses Normal quality prices for crafted items.

**8. Trip planner (trips.go)**
For each pair of cities, it takes the flips on that route and sorts them by **profit per kg**. Then it fills your bags from the top: as many of each as it can, until you run out of carry weight or silver. Item weights come from the game data. Sorting by profit per kg is a "greedy" method: it's quick, and nearly always gives the best load or close to it. The plan is worked out on the server from the carry weight and silver budget you type in, which the window sends with each update; the page just draws the result.


**9. The window (web.go and web/index.html)**
The window is a web page built into the exe. When the exe starts, it runs a tiny web server that only your own PC can reach (`127.0.0.1`), then opens it in your browser. Every 2 seconds the page asks the server for the latest status, flips and activity, and redraws. Item IDs like `T6_2H_HOLYSTAFF` are turned into names like "Master's Great Holy Staff" using the game's own item list.

## Public prices

The **Get public prices** button downloads prices for about 10,000 items in every royal city, Caerleon, Brecilien and the Black Market from the [Albion Data Project](https://www.albion-online-data.com), a free site built from other players' scans. It asks for about 75 items at a time, one request a second, to stay under the site's limit (180 a minute), so it takes about 3 minutes.

- Public prices are marked **public** in the window. They can be hours old, and they don't say how many are on offer, so flips using them count 1 item. Check them in game before buying lots.
- Your own sniffed prices always win when they're newer.

## Price history

As well as keeping the latest price, the sniffer appends every price it stores to `history.jsonl`, one JSON object per line (append-only, never rewritten). Each line has the time, item, quality, city (zone ID), side (`sell` or `buy`), price, amount, the full list of order levels (for your own scans), and the source (`own` or `public`). It skips a line if nothing changed since the last one for that item/quality/city/side, so it doesn't fill with duplicates. It's meant to be read later by other projects, like a market simulator.

- **Export history** (button in the window) writes `history-export.csv` next to the sniffer, one row per record, with the levels as a short text like `2100x4;2600x10`.
- From a terminal, `albion-sniffer.exe -export-history` does the same without opening the window.
- In the **All prices** tab, click any row to see a small chart of that item's cheapest sell and best buy price over time in that city.

## Recording game traffic

The **Record game traffic** button saves 2 minutes of raw game packets to a `.albrec` file in the sniffer's folder. It's for finding new things the sniffer could read, like your carry weight. Write down a number you can see in game (like your inventory weight), record, and then search the recording for that number.

The file holds everything the game sent in that time, which can include your character name and nearby chat. It never leaves your PC unless you send it.

To read a recording (on any computer with Go):

```
go run . -replay recording-2026-10-07-191651.albrec
```

This prints every message, its direction (`server->PC` or `PC->server`) and all its values.

## What's used and why

| Thing | Why |
|---|---|
| **Go** | Builds to one .exe with nothing to install. Fast enough to keep up with every packet. The Albion Data Project's own client is in Go too. |
| **gopacket** (Google's library) | Talks to Npcap and pulls the UDP part out of each packet. On Windows it loads Npcap by itself, so no C compiler is needed. |
| **Npcap** | Windows needs a driver to let programs see network packets. This is the standard one (Wireshark uses it). |
| **A web page for the window** | Go has no simple built-in way to make desktop windows. A web page is easy to style, needs no extra libraries, and still ships inside the one exe. |
| **The game's own data** | Item names and recipes come from the game files, published in the [ao-bin-dumps](https://github.com/ao-data/ao-bin-dumps) repo, and are built into the exe. |
| **JSON file** for prices | Simple, readable, and easy to load into your other projects later. |
| **Tests** (`sniffer_test.go`) | Builds fake Photon packets (normal, fragmented, stuck together, broken) and checks they decode right, since the real game can't be tested from the build machine. Random-data testing was also used to make sure bad packets can't crash it. |

The protocol details (header sizes, type codes, fragment layout) were learned from the open-source [Albion Data Project client](https://github.com/ao-data/albiondata-client) (MIT licence). The code here is written separately.

## If something's wrong

- **"Couldn't find any network adapters"**: Npcap isn't installed.
- **The window doesn't open**: look in the console for the `Window:` address and paste it into your browser.
- **Nothing happens when browsing the market**: run with `-debug`. If no `[debug]` lines appear at all, the adapter isn't being captured. Try running as Administrator, and turn off any VPN.
- **"The game is sending encrypted data"**: Albion sometimes encrypts live market data. While it does, no sniffer can read prices, not even the Data Project's. Nothing can be done until they turn it off.
- **Prices look 10,000× wrong**: the game stores prices × 10,000, and the sniffer divides them back. If they look off, try `-raw-prices`.
- **City says Unknown**: change zone once.
- **It says Lymhurst but no prices come in**: city markets are their own zones. Go inside the marketplace building.

## Building it yourself

```
go mod tidy
go test .
go run . -demo       # on Mac/Linux: shows the window with made-up prices
GOOS=windows GOARCH=amd64 go build -o albion-sniffer.exe .
```

Built with Claude.
