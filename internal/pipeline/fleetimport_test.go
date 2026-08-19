package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"goldstar/internal/store"
)

func writeCSV(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fleet.csv")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func openDB(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// A dispatch export is messy by nature. The importer has to be strict about
// what counts as a vehicle, because a junk row becomes a phantom car that
// costs get attributed to.
func TestImportFleetCleansAndRejects(t *testing.T) {
	csv := `Callsign,Make,Model,Registration
210,SEAT,ALHAMBRA,DK18CXR
63,toyota,CORROLA,FY69 NKR
4,TOYOTTA,PRUIS,YE69ZNO
43,TOYOTTA,AURIS,JJJ
555,Lambo,hybrid,faz
9999,Make,Model,9999
247,TOYOTA,COROLLA,ND21OJU.
26,SKODA,SUPERB,GV70HFK.
75,SKODA,SUPERB,GV70HFK
`
	db := openDB(t)
	rep, err := ImportFleetCSV(db, writeCSV(t, csv), true, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if rep.Rows != 9 {
		t.Errorf("rows = %d, want 9", rep.Rows)
	}
	// JJJ, faz and the template row must not become vehicles.
	if len(rep.Rejected) != 3 {
		t.Errorf("rejected %d rows, want 3: %v", len(rep.Rejected), rep.Rejected)
	}
	// GV70HFK twice, with and without a trailing dot, is one car.
	if rep.Vehicles != 5 {
		t.Errorf("vehicles = %d, want 5", rep.Vehicles)
	}
	if len(rep.Duplicates) != 1 {
		t.Errorf("duplicates = %v, want exactly GV70HFK", rep.Duplicates)
	}

	v, err := db.GetVehicle("GV70HFK")
	if err != nil {
		t.Fatalf("GV70HFK was not saved: %v", err)
	}
	if v.Notes != "Callsign 26, 75" {
		t.Errorf("notes = %q, want both callsigns recorded", v.Notes)
	}

	// Misspelt marques must collapse to one, or every report splits them.
	toy, err := db.GetVehicle("YE69ZNO")
	if err != nil {
		t.Fatal(err)
	}
	if toy.Make != "Toyota" {
		t.Errorf("TOYOTTA imported as %q, want Toyota", toy.Make)
	}
	lower, _ := db.GetVehicle("FY69NKR")
	if lower.Make != "Toyota" {
		t.Errorf("lowercase toyota imported as %q, want Toyota", lower.Make)
	}
}

// An import must not wipe details a person entered by hand; the export knows
// nothing about drivers.
func TestImportPreservesHumanEnteredDetail(t *testing.T) {
	db := openDB(t)
	driver := "A. Driver"
	if err := db.SaveVehicle("DK18CXR", store.VehiclePatch{Driver: &driver}); err != nil {
		t.Fatal(err)
	}

	csv := "Callsign,Make,Model,Registration\n210,SEAT,ALHAMBRA,DK18CXR\n"
	if _, err := ImportFleetCSV(db, writeCSV(t, csv), true, nil); err != nil {
		t.Fatalf("import: %v", err)
	}

	v, err := db.GetVehicle("DK18CXR")
	if err != nil {
		t.Fatal(err)
	}
	if v.Driver != driver {
		t.Errorf("driver = %q, want it preserved as %q", v.Driver, driver)
	}
	if v.Make != "SEAT" || v.Model != "Alhambra" {
		t.Errorf("specs = %q %q, want them filled in from the export", v.Make, v.Model)
	}
}

// A dry run must change nothing.
func TestImportDryRunWritesNothing(t *testing.T) {
	db := openDB(t)
	csv := "Callsign,Make,Model,Registration\n210,SEAT,ALHAMBRA,DK18CXR\n"

	rep, err := ImportFleetCSV(db, writeCSV(t, csv), false, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if rep.Applied {
		t.Error("report claims it was applied")
	}
	if _, err := db.GetVehicle("DK18CXR"); err == nil {
		t.Error("a dry run created a vehicle")
	}
}
