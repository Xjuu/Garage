package store

import (
	"errors"
	"testing"
)

// A part on the shelf plus a car in the registry to fit it to — the two
// halves every fitment check needs.
func stockFixture(t *testing.T, db *Store, fitsMake, fitsModel string) int64 {
	t.Helper()
	id, err := db.AddStockPart(StockPart{
		Barcode: "5012345678900", PartNumber: "BP-1234", Description: "Front brake pads",
		Quantity: 10, MinQuantity: 2, UnitCost: 24.50,
		FitsMake: fitsMake, FitsModel: fitsModel,
	})
	if err != nil {
		t.Fatalf("AddStockPart: %v", err)
	}
	return id
}

func TestStockPartRoundTripAndBarcodeIdentity(t *testing.T) {
	db := open(t)
	id := stockFixture(t, db, "", "")

	p, err := db.StockPart(id)
	if err != nil {
		t.Fatalf("StockPart: %v", err)
	}
	if p.Quantity != 10 || p.PartNumber != "BP-1234" {
		t.Errorf("unexpected part: %+v", p)
	}

	// A scanner is a keyboard: the same label typed with stray whitespace
	// or in lower case has to find the same row.
	for _, code := range []string{"5012345678900", " 5012345678900 "} {
		got, err := db.StockPartByBarcode(code)
		if err != nil {
			t.Errorf("StockPartByBarcode(%q): %v", code, err)
			continue
		}
		if got.ID != id {
			t.Errorf("StockPartByBarcode(%q) found part %d, want %d", code, got.ID, id)
		}
	}

	// And the same box cannot be entered twice under one barcode.
	if _, err := db.AddStockPart(StockPart{Barcode: "5012345678900"}); err == nil {
		t.Error("a duplicate barcode should be rejected")
	}
	if _, err := db.AddStockPart(StockPart{Barcode: "  "}); err == nil {
		t.Error("a part with no barcode should be rejected")
	}
	// "Fits a Corolla, any manufacturer" is not a rule that can be checked.
	if _, err := db.AddStockPart(StockPart{Barcode: "X1", FitsModel: "Corolla"}); err == nil {
		t.Error("a model restriction without a make should be rejected")
	}
}

// An opening quantity is a movement like any other, so the history adds up
// to the number on the shelf rather than starting from an unexplained one.
func TestOpeningStockIsRecordedAsAMovement(t *testing.T) {
	db := open(t)
	id := stockFixture(t, db, "", "")

	moves, err := db.StockMovements(id, 0)
	if err != nil {
		t.Fatalf("StockMovements: %v", err)
	}
	if len(moves) != 1 || moves[0].Delta != 10 {
		t.Fatalf("want one opening movement of +10, got %+v", moves)
	}
}

func TestAdjustStockAddsTakesAndRefusesToGoNegative(t *testing.T) {
	db := open(t)
	id := stockFixture(t, db, "", "")

	p, err := db.AdjustStock(id, 5, StockReceived, "", "klon", "delivery")
	if err != nil {
		t.Fatalf("AdjustStock(+5): %v", err)
	}
	if p.Quantity != 15 {
		t.Errorf("Quantity = %v after +5, want 15", p.Quantity)
	}

	p, err = db.AdjustStock(id, -3, StockUsed, "", "klon", "")
	if err != nil {
		t.Fatalf("AdjustStock(-3): %v", err)
	}
	if p.Quantity != 12 {
		t.Errorf("Quantity = %v after -3, want 12", p.Quantity)
	}

	// You cannot take more off the shelf than is on it.
	if _, err := db.AdjustStock(id, -99, StockUsed, "", "klon", ""); err == nil {
		t.Error("taking more than the shelf holds should be refused")
	}
	if p, _ := db.StockPart(id); p.Quantity != 12 {
		t.Errorf("a refused adjustment must not change the quantity, got %v", p.Quantity)
	}

	if _, err := db.AdjustStock(id, 1, "borrowed", "", "", ""); err == nil {
		t.Error("an unrecognised reason should be rejected")
	}
	if _, err := db.AdjustStock(id, 0, StockUsed, "", "", ""); err == nil {
		t.Error("a zero adjustment should be rejected")
	}
}

// The rule the tracker exists for: a part restricted to one make and model
// may only be fitted to a car the registry says is that make and model.
func TestFitmentRestrictsWhichCarAPartCanGoOn(t *testing.T) {
	db := open(t)
	id := stockFixture(t, db, "Toyota", "Corolla")

	if err := db.SaveVehicle("AA11AAA", VehiclePatch{
		Make: ptr("Toyota"), Model: ptr("Corolla"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveVehicle("BB22BBB", VehiclePatch{
		Make: ptr("Toyota"), Model: ptr("Yaris"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveVehicle("CC33CCC", VehiclePatch{
		Make: ptr("Skoda"), Model: ptr("Octavia"),
	}); err != nil {
		t.Fatal(err)
	}

	// The car it actually fits.
	if _, err := db.AdjustStock(id, -1, StockUsed, "AA11AAA", "klon", ""); err != nil {
		t.Errorf("fitting the part to the car it is for: %v", err)
	}

	// Right make, wrong model.
	_, err := db.AdjustStock(id, -1, StockUsed, "BB22BBB", "klon", "")
	if err == nil {
		t.Error("a Corolla part should be refused for a Yaris")
	}
	var fe FitmentError
	if !errors.As(err, &fe) {
		t.Errorf("want a FitmentError so the API can answer 409, got %T: %v", err, err)
	}

	// Wrong make entirely.
	if _, err := db.AdjustStock(id, -1, StockUsed, "CC33CCC", "klon", ""); err == nil {
		t.Error("a Toyota part should be refused for a Skoda")
	}

	// A car nobody has registered cannot be checked, so a restricted part
	// is refused rather than assumed to fit.
	if _, err := db.AdjustStock(id, -1, StockUsed, "ZZ99ZZZ", "klon", ""); err == nil {
		t.Error("a restricted part should be refused for a car not in the registry")
	}

	// Refusals must not have moved anything: 10 opening, one legitimate use.
	if p, _ := db.StockPart(id); p.Quantity != 9 {
		t.Errorf("Quantity = %v, want 9 — only the one allowed fitting should have counted", p.Quantity)
	}
}

func TestFitmentAtMakeLevelAndUnrestrictedParts(t *testing.T) {
	db := open(t)
	if err := db.SaveVehicle("BB22BBB", VehiclePatch{
		Make: ptr("toyota"), Model: ptr("Yaris"),
	}); err != nil {
		t.Fatal(err)
	}

	// A make with no model fits any car of that make — and the comparison
	// is case-insensitive, since "toyota" and "Toyota" reach the registry
	// from different screens typed by different people.
	anyToyota := stockFixture(t, db, "Toyota", "")
	if _, err := db.AdjustStock(anyToyota, -1, StockUsed, "BB22BBB", "klon", ""); err != nil {
		t.Errorf("a make-level part should fit any car of that make: %v", err)
	}

	// The other half of a make-level rule: a Toyota part on a Skoda. This
	// case is worth its own assertion because a part restricted to a make
	// AND model gets refused by the model check first, which would hide a
	// broken make check entirely.
	if err := db.SaveVehicle("CC33CCC", VehiclePatch{
		Make: ptr("Skoda"), Model: ptr("Octavia"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdjustStock(anyToyota, -1, StockUsed, "CC33CCC", "klon", ""); err == nil {
		t.Error("a make-level Toyota part should be refused for a Skoda")
	}

	// No restriction at all fits anything, including a car nobody has
	// registered — oil and wiper blades do not care.
	universal, err := db.AddStockPart(StockPart{Barcode: "OIL-5W30", Quantity: 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdjustStock(universal, -1, StockUsed, "ZZ99ZZZ", "klon", ""); err != nil {
		t.Errorf("an unrestricted part should fit anything: %v", err)
	}
}

// Putting stock back, or correcting a stocktake, says nothing about what
// the part fits — only taking one out for a car is a fitment question.
func TestFitmentOnlyAppliesWhenTakingAPartOut(t *testing.T) {
	db := open(t)
	id := stockFixture(t, db, "Toyota", "Corolla")
	if err := db.SaveVehicle("CC33CCC", VehiclePatch{Make: ptr("Skoda")}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.AdjustStock(id, 5, StockReceived, "CC33CCC", "klon", ""); err != nil {
		t.Errorf("receiving stock should not be fitment-checked: %v", err)
	}
	if _, err := db.AdjustStock(id, 2, StockCorrection, "CC33CCC", "klon", ""); err != nil {
		t.Errorf("a positive correction should not be fitment-checked: %v", err)
	}
}

func TestStockMovementsRecordWhoAndWhatFor(t *testing.T) {
	db := open(t)
	id := stockFixture(t, db, "", "")
	if err := db.SaveVehicle("AA11AAA", VehiclePatch{Make: ptr("Toyota")}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdjustStock(id, -2, StockUsed, "aa11 aaa", "klon", "nearside"); err != nil {
		t.Fatalf("AdjustStock: %v", err)
	}

	moves, err := db.StockMovements(id, 0)
	if err != nil {
		t.Fatalf("StockMovements: %v", err)
	}
	if len(moves) != 2 {
		t.Fatalf("want the opening stock and the use, got %d", len(moves))
	}
	m := moves[0] // newest first
	if m.Delta != -2 || m.Reason != StockUsed || m.ByUser != "klon" || m.Note != "nearside" {
		t.Errorf("unexpected movement: %+v", m)
	}
	// The registration is normalised on the way in, so it joins to the
	// registry the same way every other reg in this system does.
	if m.VehicleReg != "AA11AAA" {
		t.Errorf("VehicleReg = %q, want the normalised %q", m.VehicleReg, "AA11AAA")
	}

	recent, err := db.RecentStockMovements(0)
	if err != nil {
		t.Fatalf("RecentStockMovements: %v", err)
	}
	if len(recent) != 2 || recent[0].PartNumber != "BP-1234" {
		t.Errorf("the recent list should carry the part's details: %+v", recent)
	}
}

func TestStockLowFlagAndOverview(t *testing.T) {
	db := open(t)
	id := stockFixture(t, db, "", "") // 10 on the shelf, reorder at 2

	p, _ := db.StockPart(id)
	if p.Low {
		t.Error("10 on the shelf against a minimum of 2 is not low")
	}

	if _, err := db.AdjustStock(id, -8, StockUsed, "", "klon", ""); err != nil {
		t.Fatal(err)
	}
	p, _ = db.StockPart(id)
	if !p.Low {
		t.Error("2 on the shelf against a minimum of 2 should be low")
	}

	// A part with no minimum set is never reported low — nobody has said
	// what "enough" is for it.
	if _, err := db.AddStockPart(StockPart{Barcode: "NOMIN", Quantity: 0}); err != nil {
		t.Fatal(err)
	}
	parts, _ := db.StockParts("")
	for _, x := range parts {
		if x.Barcode == "NOMIN" && x.Low {
			t.Error("a part with no minimum should never be flagged low, even at zero")
		}
	}
	// Low stock sorts to the top, where it is worth seeing.
	if len(parts) > 0 && !parts[0].Low {
		t.Errorf("the low line should sort first, got %q", parts[0].Barcode)
	}

	o, err := db.StockOverview()
	if err != nil {
		t.Fatalf("StockOverview: %v", err)
	}
	if o.Lines != 2 || o.LowLines != 1 {
		t.Errorf("overview = %+v, want 2 lines with 1 low", o)
	}
	if o.Value != 2*24.50 {
		t.Errorf("Value = %v, want 49 (2 on the shelf at 24.50)", o.Value)
	}
}

func TestStockPartsSearch(t *testing.T) {
	db := open(t)
	stockFixture(t, db, "Toyota", "Corolla")

	for _, q := range []string{"5012345", "BP-1234", "brake", "Toyota", "corolla"} {
		got, err := db.StockParts(q)
		if err != nil {
			t.Fatalf("StockParts(%q): %v", q, err)
		}
		if len(got) != 1 {
			t.Errorf("StockParts(%q) = %d row(s), want 1", q, len(got))
		}
	}
	if got, _ := db.StockParts("nothing like this"); len(got) != 0 {
		t.Errorf("a non-matching search should return nothing, got %d", len(got))
	}
}
