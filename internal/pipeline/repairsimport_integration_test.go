package pipeline

import (
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

// buildTestWorkbook writes a small "Goldstar Service Record"-shaped xlsx —
// the same layout as the real file, built by hand so the test doesn't
// depend on any external data — and returns its path.
func buildTestWorkbook(t *testing.T) string {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()

	writeSheet := func(name string, rows [][]string) {
		idx, err := f.NewSheet(name)
		if err != nil {
			t.Fatalf("NewSheet(%s): %v", name, err)
		}
		f.SetActiveSheet(idx)
		for r, row := range rows {
			for c, v := range row {
				cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
				f.SetCellValue(name, cell, v)
			}
		}
	}

	// A normal vehicle: spec block, one full service, one timing-belt entry
	// on the same row as an unrelated mini service — proving the two tables
	// are read independently rather than paired row-for-row.
	writeSheet("AB12CDE", [][]string{
		{"Goldstar Service Record"},
		{},
		{"Registration", "AB12CDE"},
		{"Vin/Chassis number", "WVWZZZ1JZXW000001"},
		{"Make and model", "FORD TRANSIT"},
		{"Colour", "WHITE"},
		{"Cylinder capacity", "2198CC"},
		{"Spare key/Number of keys", "YES/1"},
		{"Type of fuel", "DIESEL"},
		{"Engine Number", "DK89153"},
		{"Tyre Size", "185/75/16"},
		{"Radio code", "6769"},
		{"Oil", "8.5"},
		{},
		{"", "", "", "", "Timing belt change"},
		{"Date", "Type of work", "Mileage", "", "Date", "Mileage"},
		{"10-18-19", "FULL SERVICE", "28634", "", "04-13-24", "238899KM"},
		{"05-11-20", "MINI SERVICE", "37773"},
	})

	// A vehicle with a full spec block but no service history rows at all —
	// the exact shape that used to vanish from the registry entirely.
	writeSheet("XY99ZZZ", [][]string{
		{"Goldstar Service Record"},
		{},
		{"Registration", "XY99ZZZ"},
		{"Vin/Chassis number", "SB1Z93BE50E049516"},
		{"Make and model", "TOYOTA COROLLA"},
		{"Colour", "GREY"},
		{"Cylinder capacity", "1798CC"},
		{"Spare key/Number of keys", "NO"},
		{"Type of fuel", "PETROL"},
		{"Engine Number", "2ZRW"},
		{"Tyre Size", "205/55/16"},
		{"Radio code", "N/A"},
		{},
		{},
		{"", "", "", "", "Timing belt change"},
		{"Date", "Type of work", "Mileage", "", "Date", "Mileage"},
	})

	// A blank template sheet — no registration value at all.
	writeSheet("Sheet5", [][]string{
		{"Goldstar Service Record"},
		{},
		{"Registration"},
	})

	f.DeleteSheet("Sheet1") // excelize's default sheet — not part of the fixture

	path := filepath.Join(t.TempDir(), "test-workbook.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatalf("SaveAs: %v", err)
	}
	return path
}

func TestImportRepairsXLSXDryRunWritesNothing(t *testing.T) {
	db := openDB(t)
	path := buildTestWorkbook(t)

	rep, err := ImportRepairsXLSX(db, path, false, nil)
	if err != nil {
		t.Fatalf("ImportRepairsXLSX: %v", err)
	}
	if rep.Applied {
		t.Fatalf("Applied = true on a dry run")
	}
	if rep.Vehicles != 2 {
		t.Fatalf("Vehicles = %d, want 2", rep.Vehicles)
	}
	if rep.SheetsSkipped != 1 {
		t.Fatalf("SheetsSkipped = %d, want 1 (the blank template)", rep.SheetsSkipped)
	}

	got, err := db.ListRepairsForVehicle("AB12CDE")
	if err != nil {
		t.Fatalf("ListRepairsForVehicle: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a dry run must not write anything, found %d repair(s)", len(got))
	}
}

func TestImportRepairsXLSXAppliesEverything(t *testing.T) {
	db := openDB(t)
	path := buildTestWorkbook(t)

	rep, err := ImportRepairsXLSX(db, path, true, nil)
	if err != nil {
		t.Fatalf("ImportRepairsXLSX --apply: %v", err)
	}
	// 2 service rows + 1 timing-belt row for AB12CDE, 0 for XY99ZZZ.
	if rep.Imported != 3 {
		t.Fatalf("Imported = %d, want 3", rep.Imported)
	}

	repairs, err := db.ListRepairsForVehicle("AB12CDE")
	if err != nil {
		t.Fatalf("ListRepairsForVehicle: %v", err)
	}
	if len(repairs) != 3 {
		t.Fatalf("AB12CDE repairs = %+v, want 3 rows", repairs)
	}

	v, err := db.GetVehicle("AB12CDE")
	if err != nil {
		t.Fatalf("GetVehicle: %v", err)
	}
	if v.Make != "Ford" || v.Model != "Transit" || v.VIN != "WVWZZZ1JZXW000001" || v.EngineNumber != "DK89153" {
		t.Fatalf("AB12CDE spec = %+v", v)
	}

	date, found, err := db.LastTimingBeltChange("AB12CDE")
	if err != nil {
		t.Fatalf("LastTimingBeltChange: %v", err)
	}
	if !found || date != "2024-04-13" {
		t.Fatalf("LastTimingBeltChange = %q, %v; want 2024-04-13, true — the belt table's own\n"+
			"date (04-13-24), not the mileage row it happens to sit next to (10-18-19)", date, found)
	}

	// The regression this test exists for: a vehicle with a spec block but
	// zero service rows must still be in the registry.
	xy, err := db.GetVehicle("XY99ZZZ")
	if err != nil {
		t.Fatalf("GetVehicle(XY99ZZZ): %v — a vehicle with no service history yet must still be registered from its spec block alone", err)
	}
	if xy.Make != "Toyota" || xy.Model != "Corolla" {
		t.Fatalf("XY99ZZZ spec = %+v", xy)
	}
}
