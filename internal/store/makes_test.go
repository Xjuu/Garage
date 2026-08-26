package store

import "testing"

// setVehicle is a small test helper: registers reg (if needed) with the
// given make/model via SaveVehicle, same as the Fleet page's "Add a
// vehicle" form would.
func setVehicle(t *testing.T, db *Store, reg, make_, model string) {
	t.Helper()
	if err := db.SaveVehicle(reg, VehiclePatch{Make: ptr(make_), Model: ptr(model)}); err != nil {
		t.Fatalf("SaveVehicle(%s): %v", reg, err)
	}
}

func TestMakesAndModelsAggregateAndFlagTheExpensiveModel(t *testing.T) {
	db := open(t)

	// Two Ford Focuses at a modest 100 each, one Ford Transit at a much
	// higher 500 — the Transit is the one this test expects to come back
	// flagged as costing more than the rest of the make.
	setVehicle(t, db, "FF1AAA", "Ford", "Focus")
	setVehicle(t, db, "FF2AAA", "Ford", "Focus")
	setVehicle(t, db, "FT1AAA", "Ford", "Transit")
	add(t, db, Invoice{InvoiceNumber: "F1", VehicleReg: "FF1AAA", Brutto: 100})
	add(t, db, Invoice{InvoiceNumber: "F2", VehicleReg: "FF2AAA", Brutto: 100})
	add(t, db, Invoice{InvoiceNumber: "F3", VehicleReg: "FT1AAA", Brutto: 500})

	// A make with only one model on file — nothing to compare it against,
	// so its own model row must come back with PctAboveMakeAvg == 0.
	setVehicle(t, db, "TC1AAA", "Toyota", "Corolla")
	add(t, db, Invoice{InvoiceNumber: "T1", VehicleReg: "TC1AAA", Brutto: 300})

	// An invoice against a plate that was never added to the registry at
	// all — Makes/Models fundamentally need a make/model to group by, so
	// this must be silently excluded rather than crash or show up as a
	// blank-make row.
	add(t, db, Invoice{InvoiceNumber: "U1", VehicleReg: "ZZ9UNKN", Brutto: 999})

	makes, err := db.Makes()
	if err != nil {
		t.Fatalf("Makes: %v", err)
	}
	if len(makes) != 2 {
		t.Fatalf("Makes() returned %d row(s), want 2: %+v", len(makes), makes)
	}
	// Ford: 700 total across 3 vehicles; Toyota: 300 across 1 — Ford leads.
	if makes[0].Make != "Ford" || makes[0].Vehicles != 3 || makes[0].Brutto != 700 {
		t.Errorf("makes[0] = %+v, want Ford/3 vehicles/700", makes[0])
	}
	wantFordAvg := 700.0 / 3.0
	if abs(makes[0].AvgPerVehicle-wantFordAvg) > 0.01 {
		t.Errorf("Ford AvgPerVehicle = %v, want %v", makes[0].AvgPerVehicle, wantFordAvg)
	}
	if makes[1].Make != "Toyota" || makes[1].Vehicles != 1 || makes[1].Brutto != 300 || makes[1].AvgPerVehicle != 300 {
		t.Errorf("makes[1] = %+v, want Toyota/1 vehicle/300/avg 300", makes[1])
	}

	models, err := db.Models()
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("Models() returned %d row(s), want 3: %+v", len(models), models)
	}

	byModel := map[string]ModelAgg{}
	for _, m := range models {
		byModel[m.Make+" "+m.Model] = m
	}

	transit := byModel["Ford Transit"]
	if transit.Brutto != 500 || transit.Vehicles != 1 || transit.AvgPerVehicle != 500 {
		t.Errorf("Ford Transit = %+v, want brutto 500, 1 vehicle, avg 500", transit)
	}
	if abs(transit.MakeAvgPerVehicle-wantFordAvg) > 0.01 {
		t.Errorf("Ford Transit MakeAvgPerVehicle = %v, want %v", transit.MakeAvgPerVehicle, wantFordAvg)
	}
	if transit.PctAboveMakeAvg <= 0 {
		t.Errorf("Ford Transit PctAboveMakeAvg = %v, want > 0 — it's the expensive model in its make",
			transit.PctAboveMakeAvg)
	}

	focus := byModel["Ford Focus"]
	if focus.Brutto != 200 || focus.Vehicles != 2 || focus.AvgPerVehicle != 100 {
		t.Errorf("Ford Focus = %+v, want brutto 200, 2 vehicles, avg 100", focus)
	}
	if focus.PctAboveMakeAvg >= 0 {
		t.Errorf("Ford Focus PctAboveMakeAvg = %v, want < 0 — it's cheaper than its make's average",
			focus.PctAboveMakeAvg)
	}
	// Same make, so the yardstick they're both measured against is identical.
	if abs(focus.MakeAvgPerVehicle-transit.MakeAvgPerVehicle) > 0.001 {
		t.Errorf("Focus and Transit should share the same MakeAvgPerVehicle: %v vs %v",
			focus.MakeAvgPerVehicle, transit.MakeAvgPerVehicle)
	}

	corolla := byModel["Toyota Corolla"]
	if corolla.PctAboveMakeAvg != 0 {
		t.Errorf("Toyota Corolla PctAboveMakeAvg = %v, want 0 — the only model of its make",
			corolla.PctAboveMakeAvg)
	}
	if corolla.MakeAvgPerVehicle != 300 {
		t.Errorf("Toyota Corolla MakeAvgPerVehicle = %v, want 300", corolla.MakeAvgPerVehicle)
	}

	for _, m := range models {
		if m.Make == "" || m.Model == "" {
			t.Errorf("an unregistered plate's invoice leaked into Models(): %+v", m)
		}
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
