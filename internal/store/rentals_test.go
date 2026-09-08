package store

import (
	"testing"
	"time"
)

// customer + car, the two things every hire needs, as a one-liner so the
// tests below read as what they are actually about.
func rentalFixtures(t *testing.T, db *Store) (customerID, vehicleID int64) {
	t.Helper()
	customerID, err := db.AddRentalCustomer(RentalCustomer{Name: "Alex Rider", Phone: "+447700900123"})
	if err != nil {
		t.Fatalf("AddRentalCustomer: %v", err)
	}
	vehicleID, err = db.AddRentalVehicle(RentalVehicle{
		Registration: "RE21NTL", Make: "Toyota", Model: "Corolla", DailyRate: 45,
	})
	if err != nil {
		t.Fatalf("AddRentalVehicle: %v", err)
	}
	return customerID, vehicleID
}

func TestRentalCustomerRoundTripAndSearch(t *testing.T) {
	db := open(t)
	id, err := db.AddRentalCustomer(RentalCustomer{
		Name: "  Alex Rider  ", Phone: " +447700900123 ", Email: "alex@example.com",
	})
	if err != nil {
		t.Fatalf("AddRentalCustomer: %v", err)
	}

	c, err := db.RentalCustomer(id)
	if err != nil {
		t.Fatalf("RentalCustomer: %v", err)
	}
	if c.Name != "Alex Rider" || c.Phone != "+447700900123" {
		t.Errorf("fields not trimmed on the way in: %+v", c)
	}

	if _, err := db.AddRentalCustomer(RentalCustomer{Name: "   "}); err == nil {
		t.Error("a nameless customer should be rejected")
	}

	// Search covers the three things someone at the desk has to hand.
	for _, q := range []string{"alex", "7700900", "example.com"} {
		got, err := db.RentalCustomers(q)
		if err != nil {
			t.Fatalf("RentalCustomers(%q): %v", q, err)
		}
		if len(got) != 1 || got[0].ID != id {
			t.Errorf("RentalCustomers(%q) = %d row(s), want the one customer", q, len(got))
		}
	}
	if got, _ := db.RentalCustomers("nobody"); len(got) != 0 {
		t.Errorf("a non-matching search should return nothing, got %d", len(got))
	}
}

func TestRentalVehicleNormalisesRegAndRejectsDuplicates(t *testing.T) {
	db := open(t)
	id, err := db.AddRentalVehicle(RentalVehicle{Registration: "re21 ntl", Make: "Toyota"})
	if err != nil {
		t.Fatalf("AddRentalVehicle: %v", err)
	}
	v, err := db.RentalVehicle(id)
	if err != nil {
		t.Fatalf("RentalVehicle: %v", err)
	}
	if v.Registration != "RE21NTL" {
		t.Errorf("Registration = %q, want the normalised %q", v.Registration, "RE21NTL")
	}
	if v.Status != RentalAvailable {
		t.Errorf("Status = %q, want a new car to default to available", v.Status)
	}

	// The same plate typed differently is the same car, and must not become
	// a second row in the pool.
	if _, err := db.AddRentalVehicle(RentalVehicle{Registration: "RE21-NTL"}); err == nil {
		t.Error("a duplicate registration should be rejected")
	}
	if _, err := db.AddRentalVehicle(RentalVehicle{Registration: "no-digits"}); err == nil {
		t.Error("a registration with no digits is not a plate and should be rejected")
	}
	if _, err := db.AddRentalVehicle(RentalVehicle{Registration: "AB12CDE", Status: "sold"}); err == nil {
		t.Error("an unrecognised status should be rejected, not silently stored")
	}
}

// The rule the whole feature turns on: one car cannot be in two places at
// once, and the availability list must never offer something the booking
// call would then refuse.
func TestRentalAgreementRefusesOverlappingDates(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)

	first := RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-09-10", EndsOn: "2026-09-14",
	}
	if _, err := db.CreateRentalAgreement(first); err != nil {
		t.Fatalf("CreateRentalAgreement: %v", err)
	}

	// Every way two ranges can touch: inside, straddling the start, the
	// end, swallowing it whole, and sharing exactly one day at each edge.
	for _, clash := range [][2]string{
		{"2026-09-11", "2026-09-12"}, // wholly inside
		{"2026-09-08", "2026-09-11"}, // over the start
		{"2026-09-13", "2026-09-18"}, // over the end
		{"2026-09-01", "2026-09-30"}, // swallows it
		{"2026-09-14", "2026-09-20"}, // shares the last day
		{"2026-09-05", "2026-09-10"}, // shares the first day
	} {
		_, err := db.CreateRentalAgreement(RentalAgreement{
			VehicleID: vehicleID, CustomerID: customerID,
			StartsOn: clash[0], EndsOn: clash[1],
		})
		if err == nil {
			t.Errorf("booking %s..%s should clash with 09-10..09-14", clash[0], clash[1])
		}
	}

	// Either side of it, not touching, is fine.
	for _, ok := range [][2]string{
		{"2026-09-05", "2026-09-09"},
		{"2026-09-15", "2026-09-20"},
	} {
		if _, err := db.CreateRentalAgreement(RentalAgreement{
			VehicleID: vehicleID, CustomerID: customerID,
			StartsOn: ok[0], EndsOn: ok[1],
		}); err != nil {
			t.Errorf("booking %s..%s should be allowed: %v", ok[0], ok[1], err)
		}
	}
}

func TestRentalAgreementValidatesDatesAndCarState(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)

	for _, bad := range []RentalAgreement{
		{VehicleID: vehicleID, CustomerID: customerID, StartsOn: "", EndsOn: "2026-09-14"},
		{VehicleID: vehicleID, CustomerID: customerID, StartsOn: "10/09/2026", EndsOn: "2026-09-14"},
		{VehicleID: vehicleID, CustomerID: customerID, StartsOn: "2026-09-14", EndsOn: "2026-09-10"},
	} {
		if _, err := db.CreateRentalAgreement(bad); err == nil {
			t.Errorf("bad dates %q..%q should be rejected", bad.StartsOn, bad.EndsOn)
		}
	}

	if _, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: 9999, CustomerID: customerID, StartsOn: "2026-09-10", EndsOn: "2026-09-14",
	}); err == nil {
		t.Error("booking a car that does not exist should be rejected")
	}

	// A car off the road stops being bookable without being deleted.
	if err := db.UpdateRentalVehicle(vehicleID, RentalVehicle{
		Registration: "RE21NTL", Status: RentalMaintenance, DailyRate: 45,
	}); err != nil {
		t.Fatalf("UpdateRentalVehicle: %v", err)
	}
	if _, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID, StartsOn: "2026-09-10", EndsOn: "2026-09-14",
	}); err == nil {
		t.Error("booking a car marked maintenance should be rejected")
	}
}

// Returning a car has to actually free it — the point of tracking status at
// all is that the next customer can have it.
func TestReturningOrCancellingFreesTheCar(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)

	id, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-09-10", EndsOn: "2026-09-14",
	})
	if err != nil {
		t.Fatalf("CreateRentalAgreement: %v", err)
	}

	free, err := db.AvailableRentalVehicles("2026-09-11", "2026-09-12")
	if err != nil {
		t.Fatalf("AvailableRentalVehicles: %v", err)
	}
	if len(free) != 0 {
		t.Fatalf("a booked car should not be offered as available, got %d", len(free))
	}

	// Once the keys are actually handed over the car is even more
	// unavailable than when it was merely booked — it is physically with
	// someone. Both statuses have to hold the car, not just 'booked'.
	if err := db.SetRentalAgreementStatus(id, RentalOut, ""); err != nil {
		t.Fatalf("SetRentalAgreementStatus(out): %v", err)
	}
	if free, _ := db.AvailableRentalVehicles("2026-09-11", "2026-09-12"); len(free) != 0 {
		t.Fatalf("a car that is out on hire must not be offered as available, got %d", len(free))
	}
	if _, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-09-11", EndsOn: "2026-09-12",
	}); err == nil {
		t.Fatal("a car that is out on hire must not be bookable by someone else")
	}

	if err := db.SetRentalAgreementStatus(id, RentalReturned, "2026-09-12"); err != nil {
		t.Fatalf("SetRentalAgreementStatus: %v", err)
	}
	free, err = db.AvailableRentalVehicles("2026-09-11", "2026-09-12")
	if err != nil {
		t.Fatalf("AvailableRentalVehicles: %v", err)
	}
	if len(free) != 1 {
		t.Errorf("a returned car should be available again, got %d", len(free))
	}

	// And what availability offers, booking must accept — the two read the
	// same overlap rule, and this is the test that keeps them honest.
	if _, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-09-11", EndsOn: "2026-09-12",
	}); err != nil {
		t.Errorf("the car availability just offered should be bookable: %v", err)
	}
}

func TestRentalAgreementViewJoinsNamesAndPricesTheHire(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)

	id, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-09-10", EndsOn: "2026-09-14",
	})
	if err != nil {
		t.Fatalf("CreateRentalAgreement: %v", err)
	}

	a, err := db.RentalAgreement(id)
	if err != nil {
		t.Fatalf("RentalAgreement: %v", err)
	}
	if a.CustomerName != "Alex Rider" || a.Registration != "RE21NTL" || a.Make != "Toyota" {
		t.Errorf("the view should carry the joined names: %+v", a)
	}
	// 10th to 14th inclusive is five days, and the rate is snapshotted from
	// the car rather than left at zero.
	if a.Days != 5 {
		t.Errorf("Days = %d, want 5 (inclusive of both ends)", a.Days)
	}
	if a.DailyRate != 45 || a.Total != 225 {
		t.Errorf("DailyRate/Total = %v/%v, want 45/225", a.DailyRate, a.Total)
	}

	// Re-pricing the car must not rewrite a hire already agreed.
	if err := db.UpdateRentalVehicle(vehicleID, RentalVehicle{
		Registration: "RE21NTL", Status: RentalAvailable, DailyRate: 99,
	}); err != nil {
		t.Fatalf("UpdateRentalVehicle: %v", err)
	}
	a, _ = db.RentalAgreement(id)
	if a.DailyRate != 45 {
		t.Errorf("DailyRate = %v after re-pricing the car, want the agreed 45", a.DailyRate)
	}
}

func TestRentalAgreementsFilters(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)

	out, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-09-10", EndsOn: "2026-09-14",
	})
	if err != nil {
		t.Fatalf("CreateRentalAgreement: %v", err)
	}
	if err := db.SetRentalAgreementStatus(out, RentalOut, ""); err != nil {
		t.Fatalf("SetRentalAgreementStatus: %v", err)
	}
	if _, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-10-01", EndsOn: "2026-10-03",
	}); err != nil {
		t.Fatalf("CreateRentalAgreement: %v", err)
	}

	all, _ := db.RentalAgreements(RentalAgreementsFilter{})
	if len(all) != 2 {
		t.Fatalf("no filter should return both hires, got %d", len(all))
	}
	onlyOut, _ := db.RentalAgreements(RentalAgreementsFilter{Status: RentalOut})
	if len(onlyOut) != 1 || onlyOut[0].ID != out {
		t.Errorf("status filter returned %+v, want just the one that's out", onlyOut)
	}
	// "Who had what on the 12th" — spans the date, rather than starting on it.
	onDay, _ := db.RentalAgreements(RentalAgreementsFilter{OnDate: "2026-09-12"})
	if len(onDay) != 1 || onDay[0].ID != out {
		t.Errorf("OnDate filter returned %+v, want the hire spanning that day", onDay)
	}
	if none, _ := db.RentalAgreements(RentalAgreementsFilter{OnDate: "2026-09-20"}); len(none) != 0 {
		t.Errorf("OnDate on a free day should return nothing, got %d", len(none))
	}
}

// Deleting a customer or car that has history would take the history with
// it — both refuse and say what to do instead.
func TestDeletingKeepsHireHistoryIntact(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	if _, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-09-10", EndsOn: "2026-09-14",
	}); err != nil {
		t.Fatalf("CreateRentalAgreement: %v", err)
	}

	if err := db.DeleteRentalCustomer(customerID); err == nil {
		t.Error("deleting a customer with hire history should be refused")
	}
	if err := db.DeleteRentalVehicle(vehicleID); err == nil {
		t.Error("deleting a car with hire history should be refused")
	}

	// With nothing on file, both delete cleanly.
	spare, err := db.AddRentalCustomer(RentalCustomer{Name: "Nobody"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteRentalCustomer(spare); err != nil {
		t.Errorf("deleting a customer with no history: %v", err)
	}
}

func TestRentalOverviewCounts(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	// A second car in the pool, so Fleet counts more than the one on hire.
	if _, err := db.AddRentalVehicle(RentalVehicle{Registration: "RE22NTL", DailyRate: 30}); err != nil {
		t.Fatal(err)
	}

	id, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-09-10", EndsOn: "2026-09-14",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetRentalAgreementStatus(id, RentalOut, ""); err != nil {
		t.Fatal(err)
	}

	o, err := db.RentalOverview()
	if err != nil {
		t.Fatalf("RentalOverview: %v", err)
	}
	if o.OutNow != 1 {
		t.Errorf("OutNow = %d, want 1", o.OutNow)
	}
	if o.Fleet != 2 {
		t.Errorf("Fleet = %d, want 2", o.Fleet)
	}
	if o.Customers != 1 {
		t.Errorf("Customers = %d, want 1", o.Customers)
	}
	// 5 days at 45 for the one that's out.
	if o.OutValue != 225 {
		t.Errorf("OutValue = %v, want 225", o.OutValue)
	}
}

// The counter action: this car, this person, back on this date, gone now.
func TestLendCarPutsItOutTodayAndTracksTheOdometer(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	backOn := time.Now().AddDate(0, 0, 5).Format("2006-01-02")

	id, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn, MileageNow: 12500, CourtesyForReg: "", Note: "scuff on the nearside"})
	if err != nil {
		t.Fatalf("LendCar: %v", err)
	}

	a, err := db.RentalAgreement(id)
	if err != nil {
		t.Fatalf("RentalAgreement: %v", err)
	}
	// It went out of the door as the button was pressed, so it is already
	// out rather than booked for later.
	if a.Status != RentalOut {
		t.Errorf("Status = %q, want %q", a.Status, RentalOut)
	}
	if a.StartsOn != time.Now().Format("2006-01-02") {
		t.Errorf("StartsOn = %q, want today", a.StartsOn)
	}
	if a.MileageOut != 12500 || a.Notes != "scuff on the nearside" {
		t.Errorf("the counter reading and note were not kept: %+v", a)
	}

	// The reading taken at the counter becomes the car's mileage.
	v, _ := db.RentalVehicle(vehicleID)
	if v.Mileage != 12500 {
		t.Errorf("car Mileage = %v, want the 12500 read at the counter", v.Mileage)
	}

	// And the car is off the forecourt.
	board, err := db.RentalBoard()
	if err != nil {
		t.Fatalf("RentalBoard: %v", err)
	}
	if len(board.Free) != 0 {
		t.Errorf("a car that is out should not be free to lend, got %d", len(board.Free))
	}
	if len(board.Out) != 1 || board.Out[0].CustomerName != "Alex Rider" {
		t.Errorf("the out column should name who has it: %+v", board.Out)
	}
}

func TestLendCarValidates(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	backOn := time.Now().AddDate(0, 0, 3).Format("2006-01-02")

	if _, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: "", MileageNow: 0, CourtesyForReg: "", Note: ""}); err == nil {
		t.Error("a loan with no return date should be refused")
	}
	if _, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: yesterday, MileageNow: 0, CourtesyForReg: "", Note: ""}); err == nil {
		t.Error("a return date in the past should be refused")
	}
	if _, err := db.LendCar(LendRequest{VehicleID: 9999, CustomerID: customerID, BackOn: backOn, MileageNow: 0, CourtesyForReg: "", Note: ""}); err == nil {
		t.Error("lending a car that does not exist should be refused")
	}

	// Two people cannot have the same car.
	if _, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn, MileageNow: 0, CourtesyForReg: "", Note: ""}); err != nil {
		t.Fatalf("LendCar: %v", err)
	}
	if _, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn, MileageNow: 0, CourtesyForReg: "", Note: ""}); err == nil {
		t.Error("lending a car that is already out should be refused")
	}
}

// An odometer only goes up — the same guard the repairs site applies to a
// service visit, and for the same reason.
func TestLendAndReturnRefuseMileageGoingBackwards(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	backOn := time.Now().AddDate(0, 0, 3).Format("2006-01-02")

	id, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn, MileageNow: 30000, CourtesyForReg: "", Note: ""})
	if err != nil {
		t.Fatalf("LendCar: %v", err)
	}
	if err := db.BringCarBack(id, 29000); err == nil {
		t.Error("a lower reading on return should be refused")
	}
	if err := db.BringCarBack(id, 30450); err != nil {
		t.Fatalf("BringCarBack: %v", err)
	}

	v, _ := db.RentalVehicle(vehicleID)
	if v.Mileage != 30450 {
		t.Errorf("car Mileage = %v, want the 30450 it came back on", v.Mileage)
	}
	a, _ := db.RentalAgreement(id)
	if a.Status != RentalReturned || a.ReturnedOn == "" {
		t.Errorf("the loan should be closed and dated: %+v", a)
	}

	// Back on the forecourt for the next customer.
	board, _ := db.RentalBoard()
	if len(board.Free) != 1 || len(board.Out) != 0 {
		t.Errorf("a returned car should be free again: %d free, %d out", len(board.Free), len(board.Out))
	}

	// A second loan cannot start below the reading it came back on.
	if _, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn, MileageNow: 30100, CourtesyForReg: "", Note: ""}); err == nil {
		t.Error("lending out below the recorded mileage should be refused")
	}
}

// A courtesy car records which of the customer's own cars is in the
// workshop — the thing that makes it a courtesy car rather than a hire.
func TestCourtesyCarRecordsTheCarItIsStandingInFor(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	backOn := time.Now().AddDate(0, 0, 3).Format("2006-01-02")

	id, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn, MileageNow: 0, CourtesyForReg: "ab12 cde", Note: ""})
	if err != nil {
		t.Fatalf("LendCar: %v", err)
	}
	a, _ := db.RentalAgreement(id)
	// Normalised the same way every other registration in this system is,
	// so it joins to the workshop's own records.
	if a.CourtesyForReg != "AB12CDE" {
		t.Errorf("CourtesyForReg = %q, want the normalised %q", a.CourtesyForReg, "AB12CDE")
	}

	// A plain loan leaves it empty rather than inventing a car.
	if err := db.BringCarBack(id, 0); err != nil {
		t.Fatal(err)
	}
	id2, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn, MileageNow: 0, CourtesyForReg: "", Note: ""})
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := db.RentalAgreement(id2)
	if a2.CourtesyForReg != "" {
		t.Errorf("CourtesyForReg = %q, want empty for a plain loan", a2.CourtesyForReg)
	}
}

func TestRentalStatsCountMoneyAndUtilisation(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db) // £45/day
	// A second car, so utilisation is a share rather than all-or-nothing.
	if _, err := db.AddRentalVehicle(RentalVehicle{Registration: "RE22NTL", DailyRate: 30}); err != nil {
		t.Fatal(err)
	}

	// Five days at 45 = 225, out now.
	id, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-09-10", EndsOn: "2026-09-14",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetRentalAgreementStatus(id, RentalOut, ""); err != nil {
		t.Fatal(err)
	}

	st, err := db.RentalStats()
	if err != nil {
		t.Fatalf("RentalStats: %v", err)
	}
	if st.OnHireNow != 225 {
		t.Errorf("OnHireNow = %v, want 225", st.OnHireNow)
	}
	if st.BilledAllTime != 225 {
		t.Errorf("BilledAllTime = %v, want 225", st.BilledAllTime)
	}
	// Nothing paid yet, so all of it is outstanding.
	if st.CollectedAllTime != 0 || st.OutstandingNow != 225 {
		t.Errorf("collected/outstanding = %v/%v, want 0/225", st.CollectedAllTime, st.OutstandingNow)
	}
	// One of two cars is out.
	if st.UtilisationPct != 50 {
		t.Errorf("UtilisationPct = %v, want 50", st.UtilisationPct)
	}

	// Once Stripe confirms, it moves from owed to collected.
	if err := db.MarkRentalPaid(id); err != nil {
		t.Fatal(err)
	}
	st, _ = db.RentalStats()
	if st.CollectedAllTime != 225 || st.OutstandingNow != 0 {
		t.Errorf("after payment collected/outstanding = %v/%v, want 225/0",
			st.CollectedAllTime, st.OutstandingNow)
	}

	// A cancelled hire is not money anyone ever owed.
	id2, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-10-01", EndsOn: "2026-10-02",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetRentalAgreementStatus(id2, RentalCancelled, ""); err != nil {
		t.Fatal(err)
	}
	st, _ = db.RentalStats()
	if st.BilledAllTime != 225 {
		t.Errorf("BilledAllTime = %v after a cancellation, want it unchanged at 225", st.BilledAllTime)
	}

	// The busiest car leads the earning table.
	if len(st.TopCars) == 0 || st.TopCars[0].Registration != "RE21NTL" || st.TopCars[0].Billed != 225 {
		t.Errorf("TopCars[0] = %+v, want RE21NTL having billed 225", st.TopCars)
	}
}

// The courtesy view: whose car is in the workshop, and what they are
// driving while it is.
func TestCourtesyLoansListsOnlyLiveCourtesyCars(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	other, err := db.AddRentalVehicle(RentalVehicle{Registration: "RE22NTL", DailyRate: 30})
	if err != nil {
		t.Fatal(err)
	}
	backOn := time.Now().AddDate(0, 0, 4).Format("2006-01-02")

	courtesy, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn, MileageNow: 0, CourtesyForReg: "AB12CDE", Note: ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.LendCar(LendRequest{VehicleID: other, CustomerID: customerID, BackOn: backOn, MileageNow: 0, CourtesyForReg: "", Note: ""}); err != nil {
		t.Fatal(err)
	}

	list, err := db.CourtesyLoans()
	if err != nil {
		t.Fatalf("CourtesyLoans: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want only the courtesy loan, got %d", len(list))
	}
	if list[0].CourtesyForReg != "AB12CDE" || list[0].Registration != "RE21NTL" {
		t.Errorf("want RE21NTL standing in for AB12CDE, got %+v", list[0])
	}

	// Once their own car is back and the loan is closed, it drops off.
	if err := db.BringCarBack(courtesy, 0); err != nil {
		t.Fatal(err)
	}
	if list, _ := db.CourtesyLoans(); len(list) != 0 {
		t.Errorf("a returned courtesy car should not still be listed, got %d", len(list))
	}
}

func TestPaymentAndTextStateStickToTheHire(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	backOn := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	id, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn, MileageNow: 0, CourtesyForReg: "AB12CDE", Note: ""})
	if err != nil {
		t.Fatal(err)
	}

	a, _ := db.RentalAgreement(id)
	if a.Paid || a.PaymentURL != "" || a.ReadyTextedAt != "" {
		t.Fatalf("a new hire starts unpaid and unsent: %+v", a)
	}

	if err := db.StartRentalPayment(id, "cs_test_123", "https://checkout.stripe.com/x"); err != nil {
		t.Fatal(err)
	}
	a, _ = db.RentalAgreement(id)
	if a.PaymentSession != "cs_test_123" || a.PaymentURL == "" {
		t.Errorf("the checkout page should be kept so the same link can be sent again: %+v", a)
	}
	if a.Paid {
		t.Error("opening a checkout page is not payment")
	}

	if err := db.MarkRentalPaid(id); err != nil {
		t.Fatal(err)
	}
	if a, _ = db.RentalAgreement(id); !a.Paid {
		t.Error("MarkRentalPaid did not stick")
	}

	if err := db.MarkReadyTexted(id); err != nil {
		t.Fatal(err)
	}
	if a, _ = db.RentalAgreement(id); a.ReadyTextedAt == "" {
		t.Error("the ready text should be stamped so nobody sends it twice")
	}
}

// The bill: hire, insurance, a late fee only once someone applies it, and
// whatever extras the car came back with.
func TestChargeableAddsUpEverythingOwed(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db) // £45/day
	// Ends three days ago, so it is three days late and running up a fee.
	ends := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	starts := time.Now().AddDate(0, 0, -7).Format("2006-01-02")

	id, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: starts, EndsOn: ends, Status: RentalOut,
		InsurancePerDay: 12, LateFeePerDay: 25, Deposit: 200,
	})
	if err != nil {
		t.Fatal(err)
	}

	a, err := db.RentalAgreement(id)
	if err != nil {
		t.Fatal(err)
	}
	if a.Days != 5 {
		t.Fatalf("Days = %d, want 5", a.Days)
	}
	if a.Total != 225 {
		t.Errorf("Total = %v, want 5 × 45", a.Total)
	}
	if a.Insurance != 60 {
		t.Errorf("Insurance = %v, want 5 × 12", a.Insurance)
	}
	if a.DaysLate != 3 {
		t.Errorf("DaysLate = %d, want 3", a.DaysLate)
	}
	// What it WOULD cost, without that being a debt yet.
	if a.LateFeeDue != 75 {
		t.Errorf("LateFeeDue = %v, want 3 × 25", a.LateFeeDue)
	}
	if a.LateFee != 0 {
		t.Errorf("LateFee = %v — lateness must not charge itself", a.LateFee)
	}
	if a.Chargeable != 285 {
		t.Errorf("Chargeable = %v, want 225 + 60 with no fee applied", a.Chargeable)
	}

	// Applying it is a decision someone makes.
	if err := db.ApplyLateFee(id, a.LateFeeDue); err != nil {
		t.Fatal(err)
	}
	a, _ = db.RentalAgreement(id)
	if a.LateFee != 75 || a.Chargeable != 360 {
		t.Errorf("after applying: fee %v, chargeable %v, want 75 and 360", a.LateFee, a.Chargeable)
	}

	// And waiving it is the other half of the same decision.
	if err := db.ApplyLateFee(id, 0); err != nil {
		t.Fatal(err)
	}
	if a, _ = db.RentalAgreement(id); a.Chargeable != 285 {
		t.Errorf("after waiving: chargeable %v, want 285", a.Chargeable)
	}

	// Extras — fuel, a scuff — land on the same bill.
	if err := db.SetRentalCharges(id, RentalCharges{ExtraCharges: 40, ExtraNote: "returned empty"}); err != nil {
		t.Fatal(err)
	}
	a, _ = db.RentalAgreement(id)
	if a.ExtraCharges != 40 || a.ExtraNote != "returned empty" || a.Chargeable != 325 {
		t.Errorf("extras: %+v", a)
	}
	if err := db.SetRentalCharges(id, RentalCharges{ExtraCharges: -5}); err == nil {
		t.Error("a negative extra should be refused — that is a refund, not a charge")
	}

	// The deposit is held, never part of what was charged.
	if a.Deposit != 200 {
		t.Errorf("Deposit = %v, want 200", a.Deposit)
	}
	if a.DepositReturned {
		t.Error("a new hire's deposit has not been given back")
	}
	if err := db.SetDepositReturned(id, true); err != nil {
		t.Fatal(err)
	}
	a, _ = db.RentalAgreement(id)
	if !a.DepositReturned {
		t.Error("SetDepositReturned did not stick")
	}
	if a.Chargeable != 325 {
		t.Errorf("Chargeable = %v — returning a deposit must not change the bill", a.Chargeable)
	}
}

// Lateness has two clocks: a car still out is late against today, one
// already back is late against the day it came back.
func TestLatenessStopsCountingOnceTheCarIsBack(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	starts := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	ends := time.Now().AddDate(0, 0, -6).Format("2006-01-02")
	back := time.Now().AddDate(0, 0, -4).Format("2006-01-02")

	id, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: starts, EndsOn: ends, Status: RentalOut, LateFeePerDay: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := db.RentalAgreement(id)
	if a.DaysLate != 6 {
		t.Fatalf("still out: DaysLate = %d, want 6 (against today)", a.DaysLate)
	}

	if err := db.SetRentalAgreementStatus(id, RentalReturned, back); err != nil {
		t.Fatal(err)
	}
	a, _ = db.RentalAgreement(id)
	if a.DaysLate != 2 {
		t.Errorf("returned: DaysLate = %d, want 2 (against the day it came back)", a.DaysLate)
	}
	if a.LateFeeDue != 20 {
		t.Errorf("LateFeeDue = %v, want 2 × 10", a.LateFeeDue)
	}

	// A cancelled hire is nobody's debt, however long ago its dates were.
	if err := db.SetRentalAgreementStatus(id, RentalCancelled, ""); err != nil {
		t.Fatal(err)
	}
	if a, _ = db.RentalAgreement(id); a.DaysLate != 0 || a.LateFeeDue != 0 {
		t.Errorf("a cancelled hire cannot be late: %d days, £%v", a.DaysLate, a.LateFeeDue)
	}
}

// A hire on time is not late, which is worth stating because an
// off-by-one here invents a fee out of nothing.
func TestAHireReturnedOnTimeIsNotLate(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	future := time.Now().AddDate(0, 0, 3).Format("2006-01-02")

	id, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: time.Now().Format("2006-01-02"), EndsOn: future,
		Status: RentalOut, LateFeePerDay: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := db.RentalAgreement(id)
	if a.DaysLate != 0 || a.LateFeeDue != 0 {
		t.Errorf("a hire still within its dates is not late: %d days", a.DaysLate)
	}

	// Back on the very day it was due is still on time.
	if err := db.SetRentalAgreementStatus(id, RentalReturned, future); err != nil {
		t.Fatal(err)
	}
	if a, _ = db.RentalAgreement(id); a.DaysLate != 0 {
		t.Errorf("back on the due date is on time, got %d days late", a.DaysLate)
	}
}

func TestLendCarSnapshotsTheAgreedRates(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	backOn := time.Now().AddDate(0, 0, 4).Format("2006-01-02")

	id, err := db.LendCar(LendRequest{
		VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn,
		InsurancePerDay: 12, LateFeePerDay: 25, Deposit: 150,
	})
	if err != nil {
		t.Fatalf("LendCar: %v", err)
	}
	a, _ := db.RentalAgreement(id)
	if a.InsurancePerDay != 12 || a.LateFeePerDay != 25 || a.Deposit != 150 {
		t.Errorf("the agreed rates were not kept on the hire: %+v", a)
	}

	if _, err := db.LendCar(LendRequest{
		VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn, Deposit: -1,
	}); err == nil {
		t.Error("a negative deposit should be refused")
	}
}

// Messages are kept whether or not they got through.
func TestMessageLogKeepsFailuresToo(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	backOn := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	hire, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.LogRentalMessage(RentalMessage{
		CustomerID: customerID, AgreementID: &hire, Phone: "+447700900123",
		Body: "your car is ready", ProviderSID: "SM1", SentBy: "klon",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LogRentalMessage(RentalMessage{
		CustomerID: customerID, Phone: "+447700900123", Body: "second try",
		Status: "failed", Error: "unverified number", SentBy: "klon",
	}); err != nil {
		t.Fatal(err)
	}

	msgs, err := db.RentalMessages(customerID, 0)
	if err != nil {
		t.Fatalf("RentalMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want both messages, got %d", len(msgs))
	}
	// Newest first.
	if msgs[0].Status != "failed" || msgs[0].Error != "unverified number" {
		t.Errorf("a refused message keeps why: %+v", msgs[0])
	}
	if msgs[0].AgreementID != nil {
		t.Error("a message not about a hire has no hire id")
	}
	if msgs[1].AgreementID == nil || *msgs[1].AgreementID != hire {
		t.Errorf("a message about a hire keeps that link: %+v", msgs[1])
	}
	if msgs[1].CustomerName != "Alex Rider" {
		t.Errorf("the log joins the customer's name for display, got %q", msgs[1].CustomerName)
	}

	// Another customer's log is their own.
	other, _ := db.AddRentalCustomer(RentalCustomer{Name: "Nobody"})
	if got, _ := db.RentalMessages(other, 0); len(got) != 0 {
		t.Errorf("a customer with no messages has none, got %d", len(got))
	}
	if all, _ := db.RentalMessages(0, 0); len(all) != 2 {
		t.Errorf("no customer filter returns everything, got %d", len(all))
	}
}

func TestDocumentsAttachToACustomerOrAHire(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	backOn := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	hire, err := db.LendCar(LendRequest{VehicleID: vehicleID, CustomerID: customerID, BackOn: backOn})
	if err != nil {
		t.Fatal(err)
	}

	licence, err := db.AddRentalDocument(RentalDocument{
		CustomerID: &customerID, Kind: DocLicence, Filename: "licence.jpg",
		StoredPath: "/data/rental-docs/2026/09/abc.jpg", Mime: "image/jpeg",
		Bytes: 1024, UploadedBy: "klon",
	})
	if err != nil {
		t.Fatalf("AddRentalDocument: %v", err)
	}
	if _, err := db.AddRentalDocument(RentalDocument{
		AgreementID: &hire, Kind: DocDamage, Filename: "scuff.jpg",
		StoredPath: "/data/rental-docs/2026/09/def.jpg",
	}); err != nil {
		t.Fatal(err)
	}

	// A hire's documents include the customer's own — their licence is as
	// relevant to this loan as it was to the last one.
	docs, err := db.RentalDocuments(customerID, hire)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Errorf("want both the licence and the damage photo, got %d", len(docs))
	}
	if only, _ := db.RentalDocuments(0, hire); len(only) != 1 {
		t.Errorf("the hire alone has one document, got %d", len(only))
	}

	// An unrecognised kind is filed as "other" rather than rejected: the
	// file still matters even if nobody picked a label for it.
	odd, err := db.AddRentalDocument(RentalDocument{
		CustomerID: &customerID, Kind: "banana", Filename: "x.pdf", StoredPath: "/p/x.pdf",
	})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := db.RentalDocument(odd)
	if d.Kind != DocOther {
		t.Errorf("Kind = %q, want it filed as other", d.Kind)
	}

	// A document belonging to nothing has nowhere to be found again.
	if _, err := db.AddRentalDocument(RentalDocument{
		Filename: "orphan.pdf", StoredPath: "/p/o.pdf",
	}); err == nil {
		t.Error("a document with no customer and no hire should be refused")
	}
	if _, err := db.AddRentalDocument(RentalDocument{CustomerID: &customerID}); err == nil {
		t.Error("a document with no file should be refused")
	}

	// Deleting hands back the path so the file can go too.
	path, err := db.DeleteRentalDocument(licence)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/data/rental-docs/2026/09/abc.jpg" {
		t.Errorf("DeleteRentalDocument returned %q, want the stored path", path)
	}
	if left, _ := db.RentalDocuments(customerID, 0); len(left) != 1 {
		t.Errorf("one document left for the customer, got %d", len(left))
	}
}

// The morning's three questions: who is late, who is bringing one back,
// and who is collecting one.
func TestRentalTodayIsThreeListsOfWorkNotAnInventory(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	second, _ := db.AddRentalVehicle(RentalVehicle{Registration: "RE22NTL", DailyRate: 30})
	third, _ := db.AddRentalVehicle(RentalVehicle{Registration: "RE23NTL", DailyRate: 30})
	today := time.Now().Format("2006-01-02")
	past := time.Now().AddDate(0, 0, -2).Format("2006-01-02")

	// Late: out, ended two days ago.
	late, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: past, EndsOn: past, Status: RentalOut,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Due back today.
	due, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: second, CustomerID: customerID,
		StartsOn: past, EndsOn: today, Status: RentalOut,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Booked from today, not collected yet.
	collect, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: third, CustomerID: customerID,
		StartsOn: today, EndsOn: time.Now().AddDate(0, 0, 3).Format("2006-01-02"),
		Status: RentalBooked,
	})
	if err != nil {
		t.Fatal(err)
	}

	td, err := db.RentalToday()
	if err != nil {
		t.Fatalf("RentalToday: %v", err)
	}
	if len(td.Overdue) != 1 || td.Overdue[0].ID != late {
		t.Errorf("Overdue = %+v, want just the late one", td.Overdue)
	}
	if len(td.DueToday) != 1 || td.DueToday[0].ID != due {
		t.Errorf("DueToday = %+v, want just the one due back", td.DueToday)
	}
	if len(td.GoingOut) != 1 || td.GoingOut[0].ID != collect {
		t.Errorf("GoingOut = %+v, want just the one being collected", td.GoingOut)
	}
	// A car due back today is not also late — the two lists must not
	// double-count the same job.
	if td.DueToday[0].DaysLate != 0 {
		t.Errorf("a hire due back today is not late, got %d days", td.DueToday[0].DaysLate)
	}
}

// The calendar is a planning grid: every live car is a row whether or not
// it has a booking, and only hires touching the window are drawn.
func TestRentalCalendarCoversTheWindowAndEveryLiveCar(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	if _, err := db.AddRentalVehicle(RentalVehicle{Registration: "RE22NTL"}); err != nil {
		t.Fatal(err)
	}
	// A retired car is not part of any plan.
	retired, _ := db.AddRentalVehicle(RentalVehicle{Registration: "RE23NTL"})
	if err := db.UpdateRentalVehicle(retired, RentalVehicle{
		Registration: "RE23NTL", Status: RentalRetired,
	}); err != nil {
		t.Fatal(err)
	}

	inside, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-09-10", EndsOn: "2026-09-14",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Straddles the start of the window — has to be drawn, clipped.
	straddle, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-09-01", EndsOn: "2026-09-08",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Entirely after it.
	if _, err := db.CreateRentalAgreement(RentalAgreement{
		VehicleID: vehicleID, CustomerID: customerID,
		StartsOn: "2026-10-01", EndsOn: "2026-10-05",
	}); err != nil {
		t.Fatal(err)
	}

	cal, err := db.RentalCalendar("2026-09-07", "2026-09-13")
	if err != nil {
		t.Fatalf("RentalCalendar: %v", err)
	}
	if len(cal.Cars) != 2 {
		t.Errorf("want the two live cars as rows, got %d", len(cal.Cars))
	}
	for _, c := range cal.Cars {
		if c.Status == RentalRetired {
			t.Error("a retired car should not be a row on a planning grid")
		}
	}
	ids := map[int64]bool{}
	for _, h := range cal.Hires {
		ids[h.ID] = true
	}
	if !ids[inside] || !ids[straddle] {
		t.Errorf("both the hire inside the window and the one straddling it should be drawn: %v", ids)
	}
	if len(cal.Hires) != 2 {
		t.Errorf("the hire entirely outside the window should not be: %d hires", len(cal.Hires))
	}

	// A cancelled hire holds no car, so drawing it would imply otherwise.
	if err := db.SetRentalAgreementStatus(inside, RentalCancelled, ""); err != nil {
		t.Fatal(err)
	}
	cal, _ = db.RentalCalendar("2026-09-07", "2026-09-13")
	if len(cal.Hires) != 1 {
		t.Errorf("a cancelled hire should not appear on the calendar, got %d", len(cal.Hires))
	}

	if _, err := db.RentalCalendar("2026-09-13", "2026-09-07"); err == nil {
		t.Error("a backwards window should be refused")
	}
	if _, err := db.RentalCalendar("", ""); err == nil {
		t.Error("a calendar needs dates")
	}
}
