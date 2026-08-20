package store

import "testing"

func TestStockIsInvoicedMinusTaken(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{
		InvoiceNumber: "INV-1", VehicleReg: "AB12CDE", InvoiceDate: "2026-08-01",
		Items: []Item{{PartNumber: "JRP308W", Desc: "Fixings", Quantity: 10, Netto: 9.40}},
	})

	before, err := db.StockPartByNumber("JRP308W")
	if err != nil {
		t.Fatalf("StockPartByNumber: %v", err)
	}
	if before.Stock != 10 {
		t.Fatalf("stock before any take = %v, want 10", before.Stock)
	}

	if err := db.SaveVehicle("AB12CDE", VehiclePatch{}); err != nil {
		t.Fatalf("SaveVehicle: %v", err) // the registry, not invoices, is what LogStockTake checks against
	}
	if err := db.LogStockTake("JRP308W", "AB12CDE", 3, "device-1"); err != nil {
		t.Fatalf("LogStockTake: %v", err)
	}

	after, err := db.StockPartByNumber("JRP308W")
	if err != nil {
		t.Fatalf("StockPartByNumber: %v", err)
	}
	if after.Stock != 7 {
		t.Fatalf("stock after taking 3 of 10 = %v, want 7", after.Stock)
	}
}

// Taking more than was ever invoiced is a real discrepancy — count went
// wrong somewhere, or something was taken without an invoice yet existing
// for it — and has to stay visible as a negative number, not get silently
// floored at zero and hide the problem.
func TestStockCanGoNegative(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{
		InvoiceNumber: "INV-1", VehicleReg: "AB12CDE", InvoiceDate: "2026-08-01",
		Items: []Item{{PartNumber: "JRP308W", Quantity: 2}},
	})
	db.SaveVehicle("AB12CDE", VehiclePatch{})
	if err := db.LogStockTake("JRP308W", "AB12CDE", 5, "device-1"); err != nil {
		t.Fatalf("LogStockTake: %v", err)
	}

	p, err := db.StockPartByNumber("JRP308W")
	if err != nil {
		t.Fatalf("StockPartByNumber: %v", err)
	}
	if p.Stock != -3 {
		t.Fatalf("stock = %v, want -3 (a real, visible discrepancy)", p.Stock)
	}
}

func TestLogStockTakeValidation(t *testing.T) {
	db := open(t)
	db.SaveVehicle("AB12CDE", VehiclePatch{})

	cases := []struct {
		name, part, reg string
		qty             float64
	}{
		{"empty part number", "", "AB12CDE", 1},
		{"zero quantity", "JRP308W", "AB12CDE", 0},
		{"negative quantity", "JRP308W", "AB12CDE", -1},
		{"empty registration", "JRP308W", "", 1},
		{"registration with no digits, not a real plate", "JRP308W", "NOTAPLATE", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := db.LogStockTake(c.part, c.reg, c.qty, "device-1"); err == nil {
				t.Errorf("LogStockTake(%q, %q, %v) should have been rejected", c.part, c.reg, c.qty)
			}
		})
	}
}

func TestSearchStockPartsMatchesNumberOrDescription(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{
		InvoiceNumber: "INV-1", VehicleReg: "AB12CDE", InvoiceDate: "2026-08-01",
		Items: []Item{{PartNumber: "JRP308W", Desc: "Number plate fixings screw", Quantity: 3}},
	})

	byNumber, err := db.SearchStockParts("JRP308", 10)
	if err != nil {
		t.Fatalf("SearchStockParts by number: %v", err)
	}
	if len(byNumber) != 1 || byNumber[0].PartNumber != "JRP308W" {
		t.Fatalf("search by part number = %+v", byNumber)
	}

	byDesc, err := db.SearchStockParts("fixings", 10)
	if err != nil {
		t.Fatalf("SearchStockParts by description: %v", err)
	}
	if len(byDesc) != 1 || byDesc[0].PartNumber != "JRP308W" {
		t.Fatalf("search by description = %+v", byDesc)
	}

	none, err := db.SearchStockParts("no such part anywhere", 10)
	if err != nil {
		t.Fatalf("SearchStockParts with no match: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no matches, got %+v", none)
	}
}

func TestSearchStockVehiclesSearchesTheRegistry(t *testing.T) {
	db := open(t)
	db.SaveVehicle("AB12CDE", VehiclePatch{})
	db.SaveVehicle("XY99ZZZ", VehiclePatch{})

	got, err := db.SearchStockVehicles("AB12", 10)
	if err != nil {
		t.Fatalf("SearchStockVehicles: %v", err)
	}
	if len(got) != 1 || got[0] != "AB12CDE" {
		t.Fatalf("SearchStockVehicles(AB12) = %v, want [AB12CDE]", got)
	}
}

func TestPartsDeviceLifecycle(t *testing.T) {
	db := open(t)

	active, err := db.PartsDeviceActive("unknown-device")
	if err != nil {
		t.Fatalf("PartsDeviceActive on an unknown device: %v", err)
	}
	if active {
		t.Fatalf("an unregistered device must not read as active")
	}

	if err := db.RegisterPartsDevice("dev-1", "iPhone"); err != nil {
		t.Fatalf("RegisterPartsDevice: %v", err)
	}
	active, err = db.PartsDeviceActive("dev-1")
	if err != nil || !active {
		t.Fatalf("a freshly registered device should be active: active=%v err=%v", active, err)
	}

	devices, err := db.ListPartsDevices()
	if err != nil {
		t.Fatalf("ListPartsDevices: %v", err)
	}
	if len(devices) != 1 || devices[0].ID != "dev-1" || devices[0].FirstSeen == "" {
		t.Fatalf("ListPartsDevices = %+v", devices)
	}

	if err := db.RevokePartsDevice("dev-1"); err != nil {
		t.Fatalf("RevokePartsDevice: %v", err)
	}
	active, err = db.PartsDeviceActive("dev-1")
	if err != nil {
		t.Fatalf("PartsDeviceActive after revoke: %v", err)
	}
	if active {
		t.Fatalf("a revoked device must read as inactive")
	}
}

// A second visit from the same device must move last_seen forward without
// resetting first_seen — an admin deciding whether to revoke a device wants
// to know both when it first appeared and whether it's still actually
// being used.
func TestRegisterPartsDeviceUpdatesLastSeenNotFirstSeen(t *testing.T) {
	db := open(t)
	if err := db.RegisterPartsDevice("dev-1", "iPhone"); err != nil {
		t.Fatalf("first RegisterPartsDevice: %v", err)
	}
	first, err := db.ListPartsDevices()
	if err != nil || len(first) != 1 {
		t.Fatalf("ListPartsDevices: %v %+v", err, first)
	}
	firstSeen := first[0].FirstSeen

	if err := db.RegisterPartsDevice("dev-1", "iPhone"); err != nil {
		t.Fatalf("second RegisterPartsDevice: %v", err)
	}
	second, err := db.ListPartsDevices()
	if err != nil || len(second) != 1 {
		t.Fatalf("ListPartsDevices after re-registering: %v %+v", err, second)
	}
	if second[0].FirstSeen != firstSeen {
		t.Fatalf("first_seen changed on a repeat visit: was %q, now %q", firstSeen, second[0].FirstSeen)
	}
}

func TestAllowedIPStartsClosedAndCanBeOpenedAndRevoked(t *testing.T) {
	db := open(t)

	allowed, err := db.IPAllowed("203.0.113.9")
	if err != nil {
		t.Fatalf("IPAllowed on an empty list: %v", err)
	}
	if allowed {
		t.Fatalf("an empty allow-list must deny everything — this is the fail-closed default, not a bug")
	}

	if err := db.AddAllowedIP("203.0.113.9", "workshop"); err != nil {
		t.Fatalf("AddAllowedIP: %v", err)
	}
	allowed, err = db.IPAllowed("203.0.113.9")
	if err != nil || !allowed {
		t.Fatalf("the address just added should be allowed: allowed=%v err=%v", allowed, err)
	}
	stillDenied, err := db.IPAllowed("203.0.113.99")
	if err != nil || stillDenied {
		t.Fatalf("an unrelated address must still be denied: denied=%v err=%v", stillDenied, err)
	}

	if err := db.RemoveAllowedIP("203.0.113.9"); err != nil {
		t.Fatalf("RemoveAllowedIP: %v", err)
	}
	allowed, err = db.IPAllowed("203.0.113.9")
	if err != nil || allowed {
		t.Fatalf("a removed address must go back to being denied: allowed=%v err=%v", allowed, err)
	}
}
