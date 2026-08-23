package store

import (
	"strings"
	"testing"
	"time"
)

func TestLogRepairRequiresRegistrationAndServiceType(t *testing.T) {
	db := open(t)
	cases := []struct {
		name string
		r    Repair
	}{
		{"empty registration", Repair{VehicleReg: "", ServiceType: "full"}},
		{"empty service type", Repair{VehicleReg: "AB12CDE", ServiceType: ""}},
		{"unknown service type", Repair{VehicleReg: "AB12CDE", ServiceType: "oil change"}},
		{"other with no description", Repair{VehicleReg: "AB12CDE", ServiceType: "other", ServiceTypeOther: ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := db.LogRepair(c.r, "device-1"); err == nil {
				t.Errorf("LogRepair(%+v) should have been rejected", c.r)
			}
		})
	}
}

// The worker app never sends a service_date — every visit it logs happened
// today. LogRepair must fall back to now() for that case but respect an
// explicit historical date when one is given, since that's the only way a
// backfilled import can put a 2019 visit on the vehicle's actual timeline
// instead of on the day it was imported.
func TestLogRepairDefaultsDateButRespectsAnExplicitOne(t *testing.T) {
	db := open(t)
	if _, err := db.LogRepair(Repair{VehicleReg: "AB12CDE", ServiceType: "full"}, "device-1"); err != nil {
		t.Fatalf("LogRepair: %v", err)
	}
	got, _ := db.ListRepairsForVehicle("AB12CDE")
	if len(got) != 1 || !strings.HasPrefix(got[0].ServiceDate, time.Now().UTC().Format("2006-01-02")) {
		t.Fatalf("ServiceDate = %q, want it to default to today", got[0].ServiceDate)
	}

	if _, err := db.LogRepair(Repair{VehicleReg: "AB12CDE", ServiceType: "full", ServiceDate: "2019-03-14"}, "device-1"); err != nil {
		t.Fatalf("LogRepair with an explicit date: %v", err)
	}
	got, _ = db.ListRepairsForVehicle("AB12CDE")
	// Newest-by-date first, so today's (already inserted) visit still leads
	// — the historical one just needs to have kept its own date verbatim,
	// not the day it happened to be logged.
	if len(got) != 2 || got[1].ServiceDate != "2019-03-14" {
		t.Fatalf("got = %+v, want a second row with ServiceDate 2019-03-14", got)
	}
}

func TestLogRepairStoresAFullVisit(t *testing.T) {
	db := open(t)
	id, err := db.LogRepair(Repair{
		VehicleReg: "AB12CDE", ServiceType: "full", Mileage: 45210,
		TimingBeltChanged: true, Description: "Full service, replaced timing belt and water pump",
		VIN: "WVWZZZ1JZXW000001", Make: "Volkswagen", Model: "Passat", Colour: "Silver",
		CylinderCapacity: "1968cc", SpareKeys: "2", FuelType: "Diesel",
		EngineSize: "2.0", TyreSize: "205/55R16", RadioCode: "1234", OilAmount: "4.5L 5W-30",
	}, "device-1")
	if err != nil {
		t.Fatalf("LogRepair: %v", err)
	}
	if id == 0 {
		t.Fatalf("LogRepair returned id 0")
	}

	got, err := db.ListRepairsForVehicle("AB12CDE")
	if err != nil {
		t.Fatalf("ListRepairsForVehicle: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListRepairsForVehicle = %+v, want 1 row", got)
	}
	r := got[0]
	if r.ServiceType != "full" || !r.TimingBeltChanged || r.VIN != "WVWZZZ1JZXW000001" || r.DeviceID != "device-1" {
		t.Fatalf("stored repair = %+v", r)
	}
}

func TestLogRepairWithOtherServiceTypeKeepsTheDescription(t *testing.T) {
	db := open(t)
	if _, err := db.LogRepair(Repair{
		VehicleReg: "AB12CDE", ServiceType: "other", ServiceTypeOther: "Clutch replacement",
	}, "device-1"); err != nil {
		t.Fatalf("LogRepair: %v", err)
	}
	got, err := db.ListRepairsForVehicle("AB12CDE")
	if err != nil || len(got) != 1 {
		t.Fatalf("ListRepairsForVehicle: %v %+v", err, got)
	}
	if got[0].ServiceTypeOther != "Clutch replacement" {
		t.Fatalf("ServiceTypeOther = %q", got[0].ServiceTypeOther)
	}
}

// A service type other than "other" must not carry a leftover
// ServiceTypeOther value even if the caller sent one by mistake.
func TestLogRepairClearsServiceTypeOtherWhenNotOther(t *testing.T) {
	db := open(t)
	if _, err := db.LogRepair(Repair{
		VehicleReg: "AB12CDE", ServiceType: "full", ServiceTypeOther: "should be dropped",
	}, "device-1"); err != nil {
		t.Fatalf("LogRepair: %v", err)
	}
	got, _ := db.ListRepairsForVehicle("AB12CDE")
	if got[0].ServiceTypeOther != "" {
		t.Fatalf("ServiceTypeOther = %q, want empty for a \"full\" service", got[0].ServiceTypeOther)
	}
}

func TestLogRepairUpsertsVehicleSpecWithoutBlankingExistingFields(t *testing.T) {
	db := open(t)
	if _, err := db.LogRepair(Repair{
		VehicleReg: "AB12CDE", ServiceType: "mini", Make: "Volkswagen", Colour: "Silver",
	}, "device-1"); err != nil {
		t.Fatalf("first LogRepair: %v", err)
	}
	v, err := db.GetVehicle("AB12CDE")
	if err != nil {
		t.Fatalf("GetVehicle: %v", err)
	}
	if v.Make != "Volkswagen" || v.Colour != "Silver" {
		t.Fatalf("vehicle spec after first visit = %+v", v)
	}

	// A second visit only supplies the model — make and colour, already on
	// file, must survive untouched.
	if _, err := db.LogRepair(Repair{
		VehicleReg: "AB12CDE", ServiceType: "mini", Model: "Passat",
	}, "device-1"); err != nil {
		t.Fatalf("second LogRepair: %v", err)
	}
	v, err = db.GetVehicle("AB12CDE")
	if err != nil {
		t.Fatalf("GetVehicle: %v", err)
	}
	if v.Make != "Volkswagen" || v.Colour != "Silver" || v.Model != "Passat" {
		t.Fatalf("vehicle spec after second visit = %+v, want make/colour preserved and model added", v)
	}
}

func TestListRepairsForVehicleIsNewestFirst(t *testing.T) {
	db := open(t)
	db.LogRepair(Repair{VehicleReg: "AB12CDE", ServiceType: "mini", Description: "first"}, "d1")
	db.LogRepair(Repair{VehicleReg: "AB12CDE", ServiceType: "full", Description: "second"}, "d1")

	got, err := db.ListRepairsForVehicle("AB12CDE")
	if err != nil {
		t.Fatalf("ListRepairsForVehicle: %v", err)
	}
	if len(got) != 2 || got[0].Description != "second" || got[1].Description != "first" {
		t.Fatalf("order = %+v, want newest first", got)
	}
}

// The regression this test exists for: the historical import inserts rows
// in the source spreadsheet's own row order, which is not guaranteed to be
// chronological — a real vehicle in the actual data had its January 2026
// visit's row sit above its June 2025 visit's row. Ordering by insertion
// id would show the wrong visit as "most recent" (and prefill the worker
// form from the wrong one); ordering by service_date must not.
func TestListRepairsForVehicleOrdersByDateNotInsertionOrder(t *testing.T) {
	db := open(t)
	if _, err := db.LogRepair(Repair{
		VehicleReg: "AB12CDE", ServiceType: "full", ServiceDate: "2026-01-21", Description: "later visit, inserted first",
	}, "historical-import"); err != nil {
		t.Fatalf("LogRepair: %v", err)
	}
	if _, err := db.LogRepair(Repair{
		VehicleReg: "AB12CDE", ServiceType: "mini", ServiceDate: "2025-06-03", Description: "earlier visit, inserted second",
	}, "historical-import"); err != nil {
		t.Fatalf("LogRepair: %v", err)
	}

	got, err := db.ListRepairsForVehicle("AB12CDE")
	if err != nil {
		t.Fatalf("ListRepairsForVehicle: %v", err)
	}
	if len(got) != 2 || got[0].ServiceDate != "2026-01-21" || got[1].ServiceDate != "2025-06-03" {
		t.Fatalf("order = %+v, want the January visit first regardless of which was inserted first", got)
	}
}

func TestLastTimingBeltChangeOnlyCountsBeltVisits(t *testing.T) {
	db := open(t)
	if _, found, err := db.LastTimingBeltChange("AB12CDE"); err != nil || found {
		t.Fatalf("no visits yet: found=%v err=%v", found, err)
	}

	db.LogRepair(Repair{VehicleReg: "AB12CDE", ServiceType: "mini", TimingBeltChanged: false}, "d1")
	if _, found, err := db.LastTimingBeltChange("AB12CDE"); err != nil || found {
		t.Fatalf("a visit with no belt change must not count: found=%v err=%v", found, err)
	}

	db.LogRepair(Repair{VehicleReg: "AB12CDE", ServiceType: "full", TimingBeltChanged: true}, "d1")
	date, found, err := db.LastTimingBeltChange("AB12CDE")
	if err != nil {
		t.Fatalf("LastTimingBeltChange: %v", err)
	}
	if !found || date == "" {
		t.Fatalf("found=%v date=%q, want a real date now that a belt was changed", found, date)
	}
}

func TestSearchRepairVehiclesBrowsesEverythingOnEmptyQuery(t *testing.T) {
	db := open(t)
	db.SaveVehicle("AB12CDE", VehiclePatch{})
	db.SaveVehicle("XY99ZZZ", VehiclePatch{})

	all, err := db.SearchRepairVehicles("", 10)
	if err != nil {
		t.Fatalf("SearchRepairVehicles: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("browse-all = %v, want both vehicles", all)
	}

	filtered, err := db.SearchRepairVehicles("AB12", 10)
	if err != nil || len(filtered) != 1 || filtered[0] != "AB12CDE" {
		t.Fatalf("filtered search = %v, err=%v", filtered, err)
	}
}

func TestRepairsDeviceLifecycle(t *testing.T) {
	db := open(t)
	active, err := db.RepairsDeviceActive("unknown")
	if err != nil || active {
		t.Fatalf("unknown device: active=%v err=%v", active, err)
	}

	if err := db.RegisterRepairsDevice("dev-1", "workshop tablet"); err != nil {
		t.Fatalf("RegisterRepairsDevice: %v", err)
	}
	active, err = db.RepairsDeviceActive("dev-1")
	if err != nil || !active {
		t.Fatalf("freshly registered device: active=%v err=%v", active, err)
	}

	devices, err := db.ListRepairsDevices()
	if err != nil || len(devices) != 1 || devices[0].ID != "dev-1" {
		t.Fatalf("ListRepairsDevices: %v %+v", err, devices)
	}

	if err := db.RevokeRepairsDevice("dev-1"); err != nil {
		t.Fatalf("RevokeRepairsDevice: %v", err)
	}
	active, err = db.RepairsDeviceActive("dev-1")
	if err != nil || active {
		t.Fatalf("revoked device: active=%v err=%v", active, err)
	}
}

func TestRecentRepairsIncludesDeviceLabel(t *testing.T) {
	db := open(t)
	db.RegisterRepairsDevice("dev-1", "workshop tablet")
	db.LogRepair(Repair{VehicleReg: "AB12CDE", ServiceType: "full"}, "dev-1")

	got, err := db.RecentRepairs(10)
	if err != nil || len(got) != 1 {
		t.Fatalf("RecentRepairs: %v %+v", err, got)
	}
	if got[0].DeviceName != "workshop tablet" {
		t.Fatalf("DeviceName = %q, want the registered label", got[0].DeviceName)
	}
}

// ── reg existence (the "add as a new registration?" gate) ───────────────

func TestRegExistsIsFalseForATrulyUnknownPlate(t *testing.T) {
	db := open(t)
	exists, err := db.RegExists("ZZ99ZZZ")
	if err != nil || exists {
		t.Fatalf("a plate nobody has ever seen: exists=%v err=%v", exists, err)
	}
}

func TestRegExistsIsTrueOnceInTheRegistry(t *testing.T) {
	db := open(t)
	db.SaveVehicle("AB12CDE", VehiclePatch{})
	exists, err := db.RegExists("AB12CDE")
	if err != nil || !exists {
		t.Fatalf("a registered vehicle: exists=%v err=%v", exists, err)
	}
}

func TestRegExistsIsTrueFromInvoiceHistoryAlone(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{InvoiceNumber: "INV-1", VehicleReg: "AB12CDE", InvoiceDate: "2026-08-01"})
	exists, err := db.RegExists("AB12CDE")
	if err != nil || !exists {
		t.Fatalf("a vehicle known only from an invoice, never registered: exists=%v err=%v", exists, err)
	}
}

func TestRegExistsIsTrueFromRepairHistoryAlone(t *testing.T) {
	db := open(t)
	if _, err := db.LogRepair(Repair{VehicleReg: "AB12CDE", ServiceType: "full"}, "d1"); err != nil {
		t.Fatalf("LogRepair: %v", err)
	}
	exists, err := db.RegExists("AB12CDE")
	if err != nil || !exists {
		t.Fatalf("a vehicle with a logged repair (LogRepair also registers it, but check the repairs table itself matters too): exists=%v err=%v", exists, err)
	}
}

func TestRegExistsIgnoresConfusableFormatting(t *testing.T) {
	db := open(t)
	db.SaveVehicle("AB12CDE", VehiclePatch{})
	// Lower case, spaced, hyphenated — NormalizeReg's job, exercised here
	// through RegExists so the "add as new?" gate doesn't fire over
	// formatting alone.
	exists, err := db.RegExists("ab12 cde")
	if err != nil || !exists {
		t.Fatalf("differently formatted but the same plate: exists=%v err=%v", exists, err)
	}
}

func TestRegExistsRejectsAnEmptyOrPlaceholderRegistration(t *testing.T) {
	db := open(t)
	if exists, err := db.RegExists(""); err != nil || exists {
		t.Fatalf("empty string: exists=%v err=%v", exists, err)
	}
	if exists, err := db.RegExists("-"); err != nil || exists {
		t.Fatalf("a placeholder with no digits: exists=%v err=%v", exists, err)
	}
}
