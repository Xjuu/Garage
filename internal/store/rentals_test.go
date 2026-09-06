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

	id, err := db.LendCar(vehicleID, customerID, backOn, 12500, "", "scuff on the nearside")
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

	if _, err := db.LendCar(vehicleID, customerID, "", 0, "", ""); err == nil {
		t.Error("a loan with no return date should be refused")
	}
	if _, err := db.LendCar(vehicleID, customerID, yesterday, 0, "", ""); err == nil {
		t.Error("a return date in the past should be refused")
	}
	if _, err := db.LendCar(9999, customerID, backOn, 0, "", ""); err == nil {
		t.Error("lending a car that does not exist should be refused")
	}

	// Two people cannot have the same car.
	if _, err := db.LendCar(vehicleID, customerID, backOn, 0, "", ""); err != nil {
		t.Fatalf("LendCar: %v", err)
	}
	if _, err := db.LendCar(vehicleID, customerID, backOn, 0, "", ""); err == nil {
		t.Error("lending a car that is already out should be refused")
	}
}

// An odometer only goes up — the same guard the repairs site applies to a
// service visit, and for the same reason.
func TestLendAndReturnRefuseMileageGoingBackwards(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	backOn := time.Now().AddDate(0, 0, 3).Format("2006-01-02")

	id, err := db.LendCar(vehicleID, customerID, backOn, 30000, "", "")
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
	if _, err := db.LendCar(vehicleID, customerID, backOn, 30100, "", ""); err == nil {
		t.Error("lending out below the recorded mileage should be refused")
	}
}

// A courtesy car records which of the customer's own cars is in the
// workshop — the thing that makes it a courtesy car rather than a hire.
func TestCourtesyCarRecordsTheCarItIsStandingInFor(t *testing.T) {
	db := open(t)
	customerID, vehicleID := rentalFixtures(t, db)
	backOn := time.Now().AddDate(0, 0, 3).Format("2006-01-02")

	id, err := db.LendCar(vehicleID, customerID, backOn, 0, "ab12 cde", "")
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
	id2, err := db.LendCar(vehicleID, customerID, backOn, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := db.RentalAgreement(id2)
	if a2.CourtesyForReg != "" {
		t.Errorf("CourtesyForReg = %q, want empty for a plain loan", a2.CourtesyForReg)
	}
}
