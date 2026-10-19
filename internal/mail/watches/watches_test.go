package watches_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/py"
)

func TestFindTrackingCarriers(t *testing.T) {
	for text, want := range map[string][]watches.Tracking{
		"Tracking 1Z999AA10123456784 shipped": {{Carrier: "UPS", Number: "1Z999AA10123456784"}},
		"USPS 9400111899223197428490":         {{Carrier: "USPS", Number: "9400111899223197428490"}},
		"EE123456789US":                       {{Carrier: "USPS", Number: "EE123456789US"}},
		"FedEx tracking 123456789012":         {{Carrier: "FedEx", Number: "123456789012"}},
		"tracking 123456789012":               nil,
		"DHL waybill 1234567890":              {{Carrier: "DHL", Number: "1234567890"}},
		"JD014600003456789012":                {{Carrier: "DHL", Number: "JD014600003456789012"}},
	} {
		got, err := watches.FindTracking(text)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("FindTracking(%q) = %v, %v; want %v", text, got, err, want)
		}
	}
	many, _ := watches.FindTracking("1Z999AA10123456784 1Z999AA10123456785 1Z999AA10123456786 1Z999AA10123456787 1Z999AA10123456788 1Z999AA10123456789 1Z999AA10123456780 1Z999AA10123456784")
	if len(many) != 5 {
		t.Errorf("tracking numbers kept = %d, want 5", len(many))
	}
}

func TestStatusAndOrderRef(t *testing.T) {
	for _, c := range []struct{ subject, body, want string }{
		{"Out for delivery today", "", watches.OutForDelivery},
		{"Update", "It will be delivered tomorrow", watches.Ordered},
		{"UPS: Delivered", "", watches.Delivered},
		{"x", "unable to deliver", watches.Exception},
		{"x", "running late", watches.Delayed},
		{"x", "on its way", watches.InTransit},
		{"x", "your shipment", watches.Shipped},
		{"nothing", "", watches.Ordered},
	} {
		if got, err := watches.ClassifyStatus(c.subject, c.body); err != nil || got != c.want {
			t.Errorf("ClassifyStatus(%q, %q) = %q, %v; want %q", c.subject, c.body, got, err, c.want)
		}
	}
	for text, want := range map[string]string{
		"Order #a12345 shipped": "A12345", "order number: 88-1234": "88-1234", "order details": "", "ordering ABCDEF": "",
	} {
		if got, err := watches.FindOrderRef(text); err != nil || got != want {
			t.Errorf("FindOrderRef(%q) = %q, %v; want %q", text, got, err, want)
		}
	}
}

func TestRegexPackageThenStatusFromLaterCarrierMail(t *testing.T) {
	d := dbtest.Mail(t)
	shipped := mail(t, d, 1, 3, "Order #A12345 has shipped. Track: 1Z999AA10123456784", fields{
		"subject": "Your order has shipped", "from_name": "Shoply", "from_addr": "orders@shoply.test", "from_domain": "shoply.test", "k_order": 1})
	c := &chat{}
	scan(t, d, c)
	if len(c.calls) != 0 {
		t.Errorf("the regex found it; the model was called: %v", c.calls)
	}
	if got := packages(t, d); len(got) != 1 || got[0] != (pkg{"UPS", "1Z999AA10123456784", "Shoply", "A12345", "shipped", shipped, shipped}) {
		t.Fatalf("packages = %+v", got)
	}
	delivered := mail(t, d, 2, 0, "Your package 1Z999AA10123456784 was delivered.", fields{
		"subject": "UPS: Delivered", "from_name": "UPS", "from_addr": "noreply@ups.com", "from_domain": "ups.com", "k_order": 1})
	scan(t, d, c)
	if got := packages(t, d); len(got) != 1 || got[0] != (pkg{"UPS", "1Z999AA10123456784", "Shoply", "A12345", "delivered", shipped, delivered}) {
		t.Fatalf("packages = %+v", got)
	}
	if active, err := watches.ListPackages(ctx, d.Read, env(), now, true, false); err != nil || len(active) != 0 {
		t.Errorf("a delivered package is not active: %+v, %v", active, err)
	}
	if all, err := watches.ListPackages(ctx, d.Read, env(), now, false, false); err != nil || len(all) != 1 {
		t.Errorf("all packages = %+v, %v", all, err)
	}
}

func TestPackageUpdatedDateFollowsAtriumTZ(t *testing.T) {
	d := dbtest.Mail(t)
	moment := float64(time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC).Unix())
	mail(t, d, 1, 0, "Shipped: 1Z999AA10123456784", fields{"subject": "Shipped", "k_order": 1})
	scan(t, d, &chat{})
	exec(t, d, "UPDATE packages SET updated_ts = ?", moment)
	tokyo := config.Map(map[string]string{"ATRIUM_TZ": "Asia/Tokyo"})
	got, err := watches.ListPackages(ctx, d.Read, tokyo, moment, false, false)
	if err != nil || len(got) != 1 || got[0].Updated != "2026-11-01" {
		t.Errorf("ListPackages = %+v, %v", got, err)
	}
}

func TestOlderMailDoesNotRegressStatus(t *testing.T) {
	d := dbtest.Mail(t)
	mail(t, d, 1, 0, "1Z999AA10123456784 was delivered", fields{"subject": "Delivered", "from_domain": "ups.com", "k_order": 1})
	mail(t, d, 2, 5, "Shipped: 1Z999AA10123456784", fields{"subject": "Shipped", "from_domain": "shop.test", "k_order": 1})
	scan(t, d, &chat{})
	if got := packages(t, d); len(got) != 1 || got[0].status != "delivered" {
		t.Errorf("packages = %+v", got)
	}
}

func TestLLMFallbackForOrdersWithoutCarrierNumbers(t *testing.T) {
	d := dbtest.Mail(t)
	mail(t, d, 1, 2, "Thanks! Your order number: 88-1234 is confirmed.", fields{"subject": "Order confirmation", "from_name": "Bookery", "k_order": 1})
	scan(t, d, &chat{answer: order("Bookery", "88-1234", "", "ordered")})
	if got := packages(t, d); len(got) != 1 || got[0].merchant != "Bookery" || got[0].orderRef != "88-1234" || got[0].status != "ordered" {
		t.Fatalf("packages = %+v", got)
	}
	mail(t, d, 2, 0, "Order 88-1234 has shipped with tracking FedEx 123456789012", fields{"subject": "Shipped", "from_name": "Bookery", "k_order": 1})
	scan(t, d, &chat{})
	got := packages(t, d)
	if len(got) != 1 || got[0].tracking != "123456789012" || got[0].status != "shipped" {
		t.Errorf("a later mail with the same order reference must update the package: %+v", got)
	}
}

func TestMovedMailIsNotScannedForPackagesAgain(t *testing.T) {
	d := dbtest.Mail(t)
	first := mail(t, d, 1, 2, "Thanks! Your order number: 88-1234 is confirmed.", fields{"subject": "Order confirmation", "from_name": "Bookery", "k_order": 1})
	a := order("Bookery", "88-1234", "", "ordered")
	scan(t, d, &chat{answer: a})
	exec(t, d, "UPDATE messages SET gone_at = ? WHERE id = ?", now, first)
	mail(t, d, 7, 2, "Thanks! Your order number: 88-1234 is confirmed.", fields{"subject": "Order confirmation", "from_name": "Bookery", "k_order": 1, "mailbox_id": 2, "digest": "d1"})
	c := &chat{answer: a}
	scan(t, d, c)
	if len(c.calls) != 0 || count(t, d, "SELECT COUNT(*) FROM watch_scans") != 1 {
		t.Errorf("calls = %v, scans = %d", c.calls, count(t, d, "SELECT COUNT(*) FROM watch_scans"))
	}
}

func TestLLMAnswerWithInventedNumbersIsRejected(t *testing.T) {
	d := dbtest.Mail(t)
	mail(t, d, 1, 2, "Your order is confirmed.", fields{"subject": "Order confirmation", "k_order": 1})
	scan(t, d, &chat{answer: answer("is_order", true, "carrier", "UPS", "tracking_number", "1Z000000000000000", "merchant", "X",
		"order_ref", "ZZ999", "status", "shipped", "status_text", "")})
	if n := count(t, d, "SELECT COUNT(*) FROM packages"); n != 0 {
		t.Errorf("packages = %d", n)
	}
	if n := count(t, d, "SELECT COUNT(*) FROM watch_scans WHERE found = 0"); n != 1 {
		t.Errorf("the rejected mail must be marked scanned with nothing found: %d", n)
	}
}

func TestChatDownDefersWithoutMarkingScanned(t *testing.T) {
	d := dbtest.Mail(t)
	mail(t, d, 1, 2, "Your order is confirmed.", fields{"subject": "Order confirmation", "k_order": 1})
	got := scan(t, d, &chat{err: &httpx.ServiceUnavailable{Msg: "offline"}})
	if got != (watches.ScanResult{}) || count(t, d, "SELECT COUNT(*) FROM watch_scans") != 0 {
		t.Errorf("scan = %+v, scans = %d", got, count(t, d, "SELECT COUNT(*) FROM watch_scans"))
	}
}

func TestMalformedAnswerIsMarkedScannedAndSkipped(t *testing.T) {
	d := dbtest.Mail(t)
	mail(t, d, 1, 2, "Your order is confirmed.", fields{"subject": "Order confirmation", "k_order": 1})
	mail(t, d, 2, 1, "Your order is confirmed too.", fields{"subject": "Order confirmation 2", "k_order": 1})
	for name, bad := range map[string]error{"value": httpx.ErrValue, "key": httpx.ErrKey, "type": httpx.ErrType} {
		exec(t, d, "DELETE FROM watch_scans")
		got := scan(t, d, &chat{err: bad})
		if got.Scanned != 0 || count(t, d, "SELECT COUNT(*) FROM watch_scans WHERE found = 0") != 2 {
			t.Errorf("%s: scan = %+v, scans = %d", name, got, count(t, d, "SELECT COUNT(*) FROM watch_scans"))
		}
	}
	exec(t, d, "DELETE FROM watch_scans")
	if _, err := watches.ScanPackages(ctx, d, config.Config{}, 10, now, opts(&chat{err: httpx.ErrIndex})); !errors.Is(err, httpx.ErrIndex) {
		t.Errorf("an IndexError is not a malformed answer: %v", err)
	}
}

func TestCleanPackage(t *testing.T) {
	text := "Subject\nYour order ab-12345 ships with 1Z999AA10123456784 today"
	item, ok, err := watches.CleanPackage(answer("is_order", true, "carrier", " UPS ", "tracking_number", "1z999aa1 0123456784", "merchant", "Shop  Co",
		"order_ref", "ab-12345", "status", "bogus", "status_text", "x"), text)
	want := watches.Item{Carrier: "UPS", TrackingNumber: "1z999aa10123456784", Merchant: "Shop Co", OrderRef: "AB-12345", Status: "ordered", StatusText: "x"}
	if err != nil || !ok || item != want {
		t.Errorf("CleanPackage = %+v, %v, %v; want %+v", item, ok, err, want)
	}
	for name, a := range map[string]any{
		"not an object": []any{}, "not an order": answer("is_order", false), "missing flag": answer(),
		"too short": answer("is_order", true, "tracking_number", "AB12"),
		"bad chars": answer("is_order", true, "tracking_number", "AB12/34567"),
	} {
		if _, ok, err := watches.CleanPackage(a, text); ok || err != nil {
			t.Errorf("%s: ok = %v, %v", name, ok, err)
		}
	}
	if _, ok, _ := watches.CleanPackage(answer("is_order", true, "order_ref", 12345), "ref 12345"); !ok {
		t.Error("a numeric order_ref is str()-ed")
	}
	long, _, _ := watches.CleanPackage(answer("is_order", true, "order_ref", strings.Repeat("A", 60), "merchant", strings.Repeat("m", 100)), strings.Repeat("a", 60))
	if len(long.OrderRef) != 40 || len([]rune(long.Merchant)) != 80 || !strings.HasSuffix(long.Merchant, "…") {
		t.Errorf("clipping = %d, %d", len(long.OrderRef), len([]rune(long.Merchant)))
	}
}

func TestMerchantKey(t *testing.T) {
	if got, err := watches.MerchantKey("  Shop—B & Co.  "); err != nil || got != "shop b co" {
		t.Errorf("MerchantKey = %q, %v", got, err)
	}
}

func TestDismissAndTools(t *testing.T) {
	d := dbtest.Mail(t)
	mail(t, d, 1, 1, "1Z999AA10123456784", fields{"subject": "Shipped", "k_order": 1})
	scan(t, d, &chat{})
	got, err := watches.ListPackages(ctx, d.Read, env(), now, true, false)
	if err != nil || len(got) != 1 || got[0].TrackingNumber != "1Z999AA10123456784" {
		t.Fatalf("ListPackages = %+v, %v", got, err)
	}
	if ok, err := watches.DismissPackage(ctx, d.Write, 1); err != nil || !ok {
		t.Fatalf("DismissPackage = %v, %v", ok, err)
	}
	if ok, _ := watches.DismissPackage(ctx, d.Write, 99); ok {
		t.Error("DismissPackage(99) found a package")
	}
	if got, _ := watches.ListPackages(ctx, d.Read, env(), now, true, false); len(got) != 0 {
		t.Errorf("dismissed package listed: %+v", got)
	}
	if got, _ := watches.ListPackages(ctx, d.Read, env(), now, true, true); len(got) != 1 {
		t.Errorf("include_dismissed = %+v", got)
	}
}

func TestRefreshSwallowsScanFailures(t *testing.T) {
	d := dbtest.Mail(t)
	mail(t, d, 1, 1, "1Z999AA10123456784", fields{"subject": "Shipped", "k_order": 1})
	boom := func(context.Context, string, string, string, any, int) (any, error) { return nil, errors.New("bad") }
	exec(t, d, "UPDATE messages SET subject = 'Your order', k_order = 1, from_domain = 'x.test'")
	exec(t, d, "UPDATE message_fts SET body = 'no number here'")
	out := watches.Refresh(ctx, d, config.Config{}, watches.ScanLimit, now, watches.Options{Chat: boom, Clock: func() float64 { return now }, Env: env()})
	if got := py.JSONDumps(out, py.JSONOpts{Compact: true}); got != `{"packages":{"error":true}}` {
		t.Errorf("Refresh = %s", got)
	}
	ok := watches.Refresh(ctx, d, config.Config{}, watches.ScanLimit, now, watches.Options{Chat: (&chat{}).call, Env: env()})
	if got := py.JSONDumps(ok, py.JSONOpts{Compact: true}); got != `{"packages":{"scanned":1,"found":0}}` {
		t.Errorf("Refresh = %s", got)
	}
}

func TestPackageSchemaAndHint(t *testing.T) {
	if !strings.HasPrefix(watches.PackageHint, "m.k_order = 1 OR lower(m.subject) LIKE '%shipped%' OR") || !strings.HasSuffix(watches.PackageHint, "LIKE '%your package%'") {
		t.Errorf("hint = %s", watches.PackageHint)
	}
	raw, err := json.Marshal(watches.PackageSchema)
	if err != nil || !strings.Contains(string(raw), `"enum":["ordered","shipped","in_transit","out_for_delivery","delivered","delayed","exception"]`) {
		t.Errorf("schema = %s, %v", raw, err)
	}
}
