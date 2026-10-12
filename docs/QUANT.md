# The quant layer — market maths, in plain English

This explains the extra numbers Albion Econ Manager works out from your price
**history** (not just the price right now). The aim is to understand the market
a bit like a trader does: is this price calm or wild? Is it unusually cheap
today, or normal? Is it drifting up or down? Is a flip's profit dependable or
just a lucky spike? How much can I actually buy before I flood the market?

All of it is built from the **daily summaries** the tool already saves (one
min/median/max per item, per quality, per city, per side, per day). Nothing
here reads the game or changes how prices are downloaded.

**A promise about honesty:** none of these numbers are made up. If there isn't
enough history to work one out (too few days, or the price never moved), the
tool shows "–" or "not enough yet" instead of guessing. Early on you'll see a
lot of "–"; that's correct — it means *we genuinely don't know yet*.

The layer is being built in five parts:

1. **Volatility** — how much a price bounces around *(this doc so far)*
2. Z-score — how unusual today's price is
3. Trend — which way the price is heading
4. Risk-adjusted flip score — steady profit vs lucky spikes
5. Liquidity cap — how much the market can actually absorb

---

## A few words first

**Average (mean).** Add the numbers up, divide by how many there are. The
average of 4, 6, 8 is 6.

**Standard deviation (std dev).** A measure of how *spread out* numbers are —
roughly, the typical distance of a value from the average. If every day's price
is almost the same, the standard deviation is small. If prices are all over the
place, it's large. Prices 100, 100, 100 have a std dev of 0 (no spread); prices
50, 100, 150 have a big one.

**Log return.** A fancy name for "how much did the price grow from one day to
the next", written with a logarithm: `ln(today ÷ yesterday)`. We use the log
version because it behaves nicely — a +10% day and a −10% day cancel out
properly, and the daily growths add up cleanly over time. A log return near 0
means little change; a big positive one means a jump up; a big negative one a
drop. (You don't need to understand logarithms to use the tool — just know a
log return is "one day's price move".)

---

## 1. Volatility — how much a price bounces around

**What it means.** How much an item's price typically moves from day to day. A
**steady** item barely moves (safe to flip — the price you see is likely the
price you'll get). A **jumpy** item swings a lot (riskier — it can move against
you between buying and selling).

**The formula.**

1. Take the item's daily median prices over the last 14 days, oldest first.
2. Turn them into daily **log returns**: `ln(day ÷ day before)`.
3. Volatility = the **standard deviation** of those daily returns, as a percent
   (× 100). It's the typical size of a one-day price move.

**Labels (the thresholds):**

| Daily volatility | Label  | Meaning |
|------------------|--------|---------|
| under 5%         | steady | barely moves day to day |
| 5% – 15%         | normal | moves a bit, as most items do |
| over 15%         | jumpy  | swings a lot — treat its price as uncertain |

**Not enough yet:** needs at least 5 days of data. If the price never changed
(std dev 0), it shows "–" too — that almost always means we've only caught a
couple of real updates, not that the item is genuinely frozen.

**Worked example.** Say Adept's Bag's daily median sell price over five days was:

```
Day 1   2,000
Day 2   2,200   (up)
Day 3   2,100   (down)
Day 4   2,300   (up)
Day 5   2,160   (down)
```

Daily log returns:
```
ln(2200/2000) = +0.0953   (≈ +10%)
ln(2100/2200) = −0.0465   (≈ −4.7%)
ln(2300/2100) = +0.0910   (≈ +9.5%)
ln(2160/2300) = −0.0628   (≈ −6.1%)
```
The standard deviation of those four moves is about **0.0856**, i.e. **≈ 8.6%**.
That's in the 5–15% band, so the bag is labelled **normal** — it moves around a
bit day to day, which is typical. A refined resource that sat near 2,000 every
single day would come out under 5% → **steady**; a hyped weapon lurching between
40M and 60M would be over 15% → **jumpy**.

**How a trader uses it.** Volatility is your "how much do I trust this price"
gauge. A fat profit on a **jumpy** item is less trustworthy than a smaller
profit on a **steady** one, because the jumpy price might already have moved by
the time you get there. Prefer steady items when you want a sure thing; the
jumpy ones are where bigger (but riskier) swings live. In the window it sits in
the **Confidence** column, next to how reliable the flip has been.

*(Code: `volatilityPct`, `logReturns`, `stdev`, `volLabel` in `quant.go`.)*
