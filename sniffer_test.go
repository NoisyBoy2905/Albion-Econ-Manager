package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These helpers build fake Photon packets, the same shape the game sends,
// so the decoder can be tested without the game running.

func varint(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func pStr(s string) []byte { return append(varint(uint64(len(s))), s...) }

func pStrArray(list []string) []byte {
	out := []byte{tArray | tString}
	out = append(out, varint(uint64(len(list)))...)
	for _, s := range list {
		out = append(out, pStr(s)...)
	}
	return out
}

// response message: signal, type, op code, return code, extra value, params
func responseMsg(code byte, extra []byte, params []byte) []byte {
	out := []byte{0, msgResponse, code, 0, 0}
	out = append(out, extra...)
	return append(out, params...)
}

func command(cmdType byte, body []byte) []byte {
	h := make([]byte, cmdHeadLen)
	h[0] = cmdType
	binary.BigEndian.PutUint32(h[4:], uint32(cmdHeadLen+len(body)))
	return append(h, body...)
}

func packet(cmds ...[]byte) []byte {
	h := make([]byte, headerLen)
	h[3] = byte(len(cmds))
	for _, c := range cmds {
		h = append(h, c...)
	}
	return h
}

const orderA = `{"Id":1,"ItemTypeId":"T4_BAG","LocationId":null,"QualityLevel":1,"EnchantmentLevel":0,"UnitPriceSilver":25000000,"Amount":3,"AuctionType":"offer"}`
const orderB = `{"Id":2,"ItemTypeId":"T4_BAG","LocationId":null,"QualityLevel":1,"EnchantmentLevel":0,"UnitPriceSilver":21000000,"Amount":1,"AuctionType":"offer"}`
const orderC = `{"Id":4,"ItemTypeId":"T4_BAG","LocationId":null,"QualityLevel":1,"EnchantmentLevel":0,"UnitPriceSilver":25000000,"Amount":2,"AuctionType":"offer"}`
const orderBM = `{"Id":3,"ItemTypeId":"T4_BAG","LocationId":null,"QualityLevel":1,"EnchantmentLevel":0,"UnitPriceSilver":30000000,"Amount":5,"AuctionType":"request"}`

func collect(t *testing.T, payloads ...[]byte) []Message {
	t.Helper()
	var got []Message
	p := NewPhoton()
	p.OnMessage = func(m Message) { got = append(got, m) }
	for _, b := range payloads {
		p.Feed(b)
	}
	return got
}

func TestJoinGivesCity(t *testing.T) {
	// params: count 3, then key/type/value. Mix of types before the city.
	params := []byte{3, 1, tVarInt}
	params = append(params, varint(42)...)
	params = append(params, 2, tString)
	params = append(params, pStr("Micha")...)
	params = append(params, 8, tString)
	params = append(params, pStr("3005")...)
	msg := responseMsg(2, []byte{tNull}, params)

	got := collect(t, packet(command(cmdReliable, msg)))
	if len(got) != 1 || got[0].Params[8] != "3005" {
		t.Fatalf("expected city 3005, got %+v", got)
	}
}

func TestOrdersInExtraValue(t *testing.T) {
	msg := responseMsg(77, pStrArray([]string{orderA, orderB}), []byte{0})
	got := collect(t, packet(command(cmdReliable, msg)))
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	orders := parseOrders(got[0].Extra)
	if len(orders) != 2 || orders[1].Price != 21000000 {
		t.Fatalf("orders not read right: %+v", orders)
	}
}

func TestFragmentsAreJoined(t *testing.T) {
	msg := responseMsg(77, pStrArray([]string{orderA, orderB, orderBM}), []byte{0})
	half := len(msg) / 2
	frag := func(off int, chunk []byte) []byte {
		h := make([]byte, fragHeadLen)
		binary.BigEndian.PutUint32(h[0:], 9) // start sequence
		binary.BigEndian.PutUint32(h[4:], 2) // fragment count
		binary.BigEndian.PutUint32(h[12:], uint32(len(msg)))
		binary.BigEndian.PutUint32(h[16:], uint32(off))
		return append(h, chunk...)
	}
	// send the second half first, like the network sometimes does
	got := collect(t,
		packet(command(cmdFragment, frag(half, msg[half:]))),
		packet(command(cmdFragment, frag(0, msg[:half]))),
	)
	if len(got) != 1 || len(parseOrders(got[0].Extra)) != 3 {
		t.Fatalf("fragments not joined: %+v", got)
	}
}

func TestPacketsStuckTogether(t *testing.T) {
	a := packet(command(cmdReliable, responseMsg(77, pStrArray([]string{orderA}), []byte{0})))
	b := packet(command(cmdUnreliable, append([]byte{0, 0, 0, 1}, responseMsg(77, pStrArray([]string{orderB}), []byte{0})...)))
	got := collect(t, append(a, b...))
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
}

func TestEncryptedIsReported(t *testing.T) {
	p := NewPhoton()
	hits := 0
	p.OnEncrypted = func() { hits++ }
	p.Feed(packet(command(cmdReliable, []byte{0, msgEncrypted, 1, 2, 3})))
	if hits != 1 {
		t.Fatal("encrypted message not reported")
	}
}

func TestBrokenPacketDoesNotCrash(t *testing.T) {
	good := packet(command(cmdReliable, responseMsg(77, pStrArray([]string{orderA}), []byte{0})))
	for cut := 0; cut < len(good); cut++ {
		collect(t, good[:cut])
	}
}

func TestFlips(t *testing.T) {
	dir := t.TempDir()
	b := NewBook(filepath.Join(dir, "prices.json"))
	b.Add(parseOrders([]string{orderA, orderB, orderC}), "3005", true)
	b.Add(parseOrders([]string{orderBM}), "3003", true)

	flips := b.Flips(0.04, time.Hour)
	if len(flips) != 1 {
		t.Fatalf("expected 1 flip, got %+v", flips)
	}
	f := flips[0]
	// buy at 2,100 (cheapest), sell at 3,000 minus 4% = 2,880, profit 780
	if f.BuyFor != 2100 || f.SellFor != 3000 || f.Profit != 780 || f.To != "3003" {
		t.Fatalf("wrong flip: %+v", f)
	}
	// 1 bag at 2,100 and 5 at 2,500 for sale, 5 wanted at 3,000:
	// 1 x 780 + 4 x 380 = 2,300 profit on 5 bags
	if f.Qty != 5 || f.Total != 2300 {
		t.Fatalf("wrong quantity: got %d bags, %d total", f.Qty, f.Total)
	}
	if _, err := os.Stat(filepath.Join(dir, "prices.json")); err != nil {
		t.Fatal("prices.json not saved")
	}
	if len(NewBook(filepath.Join(dir, "prices.json")).Prices) != 2 {
		t.Fatal("saved prices didn't load back")
	}
}

func FuzzFeed(f *testing.F) {
	f.Add(packet(command(cmdReliable, responseMsg(77, pStrArray([]string{orderA}), []byte{0}))))
	f.Fuzz(func(t *testing.T, b []byte) {
		p := NewPhoton()
		p.OnMessage = func(m Message) { parseOrders(m.Extra) }
		p.Feed(b)
	})
}

func TestMatchStopsWhenNotWorthIt(t *testing.T) {
	sells := []Level{{100, 2}, {200, 5}}
	buys := []Level{{250, 3}, {150, 10}}
	// 2 at 100 into 250 (+150 each), 1 at 200 into 250 (+50), then 200 into 150 loses
	qty, total, cost := match(sells, buys, 0)
	if qty != 3 || total != 350 || cost != 400 {
		t.Fatalf("got %d items, %d profit", qty, total)
	}
	if sells[0].Amount != 2 {
		t.Fatal("match changed the saved order list")
	}
}

func TestListFlipWhenItPaysMore(t *testing.T) {
	dir := t.TempDir()
	b := NewBook(filepath.Join(dir, "p.json"))
	o := func(item string, price int64, kind string, amount int) Order {
		return Order{Item: item, Quality: 1, Price: price, Amount: amount, Type: kind}
	}
	// Lymhurst: cheap to buy. Caerleon: a weak buy order but a high listing.
	b.Add([]Order{o("T4_BAG", 1000, "offer", 10)}, "1002", false)
	b.Add([]Order{o("T4_BAG", 1200, "request", 5), o("T4_BAG", 2000, "offer", 3)}, "3005", false)

	flips := b.Flips(0.04, time.Hour)
	if len(flips) != 1 {
		t.Fatalf("expected 1 flip, got %+v", flips)
	}
	f := flips[0]
	// instant into the 1,200 buy order: 1,200 - 4% = 1,152 - 1,000 = 152.
	// listing at 2,000: 2,000 - 6.5% = 1,869 - 1,000 = 869. Listing wins.
	if f.Mode != "list" || f.SellFor != 2000 || f.Profit != 869 {
		t.Fatalf("expected a listing flip at 2,000: %+v", f)
	}
	// 10 on offer in Lymhurst, all under the 1,869 you'd net, so all 10.
	if f.Qty != 10 || f.Total != 8690 {
		t.Fatalf("wrong listing quantity: %d bags, %d total", f.Qty, f.Total)
	}
}

func TestCrafting(t *testing.T) {
	recipes = map[string]Recipe{
		"TEST_STAFF":   {Makes: 1, Materials: [][]any{{"TEST_PLANKS", 2.0, 1.0}, {"TEST_ARTEFACT", 1.0, 0.0}}},
		"TEST_MISSING": {Makes: 1, Materials: [][]any{{"NOT_SEEN", 1.0, 1.0}}},
	}
	b := NewBook(filepath.Join(t.TempDir(), "p.json"))
	o := func(item string, price int64, kind string) Order {
		return Order{Item: item, Quality: 1, Price: price, Amount: 1, Type: kind}
	}
	b.Add([]Order{o("TEST_PLANKS", 100, "offer"), o("TEST_ARTEFACT", 500, "offer"), o("TEST_MISSING", 9999, "request")}, "1002", false)
	b.Add([]Order{o("TEST_STAFF", 1000, "request"), o("TEST_STAFF", 1200, "offer")}, "3003", false)

	crafts := b.Crafts(0.04, 0.2, 0, time.Hour)
	if len(crafts) != 1 {
		t.Fatalf("expected only the staff, got %+v", crafts)
	}
	c := crafts[0]
	// planks: 2 x 100 with 20% back = 160, artefact never comes back = 500
	if c.Cost != 660 {
		t.Fatalf("cost should be 660, got %d", c.Cost)
	}
	// instant: 1000 - 4% = 960 - 660 = 300. listing: 1200 - 6.5% = 1122 - 660 = 462
	if c.Profit != 300 || c.ListProfit != 462 {
		t.Fatalf("wrong profits: %d instant, %d listed", c.Profit, c.ListProfit)
	}
}

func TestStationFee(t *testing.T) {
	recipes = map[string]Recipe{
		"TEST_STAFF": {Makes: 1, Value: 2048, Materials: [][]any{{"TEST_PLANKS", 20.0, 1.0}}},
	}
	b := NewBook(filepath.Join(t.TempDir(), "p.json"))
	b.Add([]Order{{Item: "TEST_PLANKS", Quality: 1, Price: 100, Amount: 50, Type: "offer"},
		{Item: "TEST_STAFF", Quality: 1, Price: 5000, Amount: 1, Type: "request"}}, "1002", false)
	// nutrition = 2048 x 0.1125 = 230.4, at 500 silver per 100 nutrition = 1,152
	c := b.Crafts(0, 0, 500, time.Hour)[0]
	if c.Fee != 1152 || c.Cost != 2000+1152 {
		t.Fatalf("fee %d, cost %d", c.Fee, c.Cost)
	}
}

func TestRecordingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	r, err := startRecording(60)
	if err != nil {
		t.Fatal(err)
	}
	msg := packet(command(cmdReliable, responseMsg(77, pStrArray([]string{orderA}), []byte{0})))
	r.write(Packet{Data: msg, FromServer: true})
	r.write(Packet{Data: []byte{1, 2, 3}, FromServer: false})
	r.close()

	var got []Packet
	if err := readRecording(r.Path, func(p Packet, _ time.Time) { got = append(got, p) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].FromServer || got[1].FromServer || len(got[0].Data) != len(msg) {
		t.Fatalf("recording didn't read back right: %+v", got)
	}
}

func TestStateNeverSendsNull(t *testing.T) {
	recipes = map[string]Recipe{}
	app := NewApp(filepath.Join(t.TempDir(), "p.json"))
	data, _ := json.Marshal(app.state(0.04, 0.152, 0, time.Hour))
	for _, k := range []string{`"flips":null`, `"crafts":null`, `"events":null`, `"all":null`} {
		if strings.Contains(string(data), k) {
			t.Fatalf("window would break on %s", k)
		}
	}
}

func TestOrdersWithoutCityAreSkipped(t *testing.T) {
	app := NewApp(filepath.Join(t.TempDir(), "p.json"))
	app.handle(Message{Kind: msgResponse, Extra: []string{orderA}})
	if app.book.Count() != 0 {
		t.Fatal("saved prices without knowing the city")
	}
}

func TestPublicPrices(t *testing.T) {
	recipes = map[string]Recipe{}
	itemNames = map[string]string{"T4_BAG": "Adept's Bag", "T4_CAPE": "Adept's Cape"}
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		now := time.Now().UTC().Add(-10 * time.Minute).Format("2006-01-02T15:04:05")
		fmt.Fprintf(w, `[
			{"item_id":"T4_BAG","city":"Lymhurst","quality":1,"sell_price_min":4358,"sell_price_min_date":"%s","buy_price_max":0,"buy_price_max_date":"0001-01-01T00:00:00"},
			{"item_id":"T4_BAG","city":"Black Market","quality":1,"sell_price_min":0,"sell_price_min_date":"0001-01-01T00:00:00","buy_price_max":5056,"buy_price_max_date":"%s"},
			{"item_id":"T4_BAG","city":"Somewhere Else","quality":1,"sell_price_min":1,"sell_price_min_date":"%s"}
		]`, now, now, now)
	}))
	defer srv.Close()

	app := NewApp(filepath.Join(t.TempDir(), "p.json"))
	// your own, newer Lymhurst price must not be replaced
	app.book.Add([]Order{{Item: "T4_BAG", Quality: 1, Price: 4000, Amount: 3, Type: "offer"}}, "1002", false)
	app.fetchPublic(srv.URL, 0)

	st := app.publicState()
	if st.Running || st.Error != "" || st.Done != st.Total || len(asked) != st.Total {
		t.Fatalf("download didn't finish right: %+v", st)
	}
	flips := app.book.Flips(0.04, time.Hour)
	if len(flips) != 1 || flips[0].BuyFor != 4000 || flips[0].SellFor != 5056 || !flips[0].Public {
		t.Fatalf("expected own buy price + public Black Market price, got %+v", flips)
	}
}

// The shipped game data must let Dual Swords flow through names, weights
// and crafting like any other weapon. This guards against a future
// ao-bin-dumps regeneration silently dropping them.
func TestDualSwordsAreInTheData(t *testing.T) {
	loadItemNames()
	loadRecipes()
	for _, id := range []string{"T4_2H_DUALSWORD", "T5_2H_DUALSWORD", "T8_2H_DUALSWORD"} {
		if itemName(id) == id {
			t.Fatalf("%s has no name in items.txt", id)
		}
		if itemWeight(id) <= 0 {
			t.Fatalf("%s has no weight in items.txt", id)
		}
		r, ok := recipes[id]
		if !ok || len(r.Materials) == 0 {
			t.Fatalf("%s has no recipe in recipes.json", id)
		}
		for _, m := range r.Materials {
			if mid, _ := m[0].(string); itemName(mid) == mid {
				t.Fatalf("%s needs %s, which has no name in items.txt", id, mid)
			}
		}
	}
}

// A Dual Swords flip and craft, the same shape as the -demo data: buy the
// sword cheap in Lymhurst, sell it to a Black Market buy order, or craft it
// from bars and leather.
func TestDualSwordsFlipAndCraft(t *testing.T) {
	recipes = map[string]Recipe{
		"T5_2H_DUALSWORD": {Makes: 1, Materials: [][]any{
			{"T5_METALBAR", 20.0, 1.0}, {"T5_LEATHER", 12.0, 1.0}}},
	}
	b := NewBook(filepath.Join(t.TempDir(), "p.json"))
	o := func(item string, price int64, kind string, amount int) Order {
		return Order{Item: item, Quality: 1, Price: price, Amount: amount, Type: kind}
	}
	b.Add([]Order{
		o("T5_2H_DUALSWORD", 48000, "offer", 2),
		o("T5_METALBAR", 1100, "offer", 800),
		o("T5_LEATHER", 1100, "offer", 500),
	}, "1002", false)
	b.Add([]Order{o("T5_2H_DUALSWORD", 55000, "request", 3)}, "3003", false)

	var f Flip
	for _, x := range b.Flips(0.04, time.Hour) {
		if x.Item == "T5_2H_DUALSWORD" {
			f = x
		}
	}
	// buy 48,000 in Lymhurst, sell to the 55,000 buy order minus 4% = 52,800
	if f.From != "1002" || f.To != "3003" || f.BuyFor != 48000 || f.SellFor != 55000 || f.Profit != 4800 {
		t.Fatalf("wrong Dual Swords flip: %+v", f)
	}
	// 2 on offer, 3 wanted: 2 x 4,800 = 9,600
	if f.Qty != 2 || f.Total != 9600 {
		t.Fatalf("wrong Dual Swords flip quantity: %+v", f)
	}

	var c Craft
	for _, x := range b.Crafts(0.04, 0.152, 0, time.Hour) {
		if x.ID == "T5_2H_DUALSWORD" {
			c = x
		}
	}
	// 20 bars x 1,100 + 12 leather x 1,100, both returnable at 15.2% back:
	// (22,000 + 13,200) x 0.848 = 29,849
	if c.Cost != 29849 {
		t.Fatalf("wrong Dual Swords craft cost: %d", c.Cost)
	}
	// instant: 55,000 - 4% = 52,800 - 29,849 = 22,951
	// listing: 48,000 - 6.5% = 44,880 - 29,849 = 15,031
	if c.Profit != 22951 || c.ListProfit != 15031 {
		t.Fatalf("wrong Dual Swords craft profits: %d instant, %d listed", c.Profit, c.ListProfit)
	}
}

func TestBatchesStayShort(t *testing.T) {
	var ids []string
	for i := 0; i < 500; i++ {
		ids = append(ids, fmt.Sprintf("T4_ITEM_NUMBER_%d", i))
	}
	total := 0
	for _, b := range batches(ids, 300) {
		if len(strings.Join(b, ",")) > 300 {
			t.Fatal("batch too long")
		}
		total += len(b)
	}
	if total != 500 {
		t.Fatal("lost items")
	}
}
