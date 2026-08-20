package store

import (
	"path/filepath"
	"testing"
)

// The point of ensureVehicleRegistered is that a plate's very first invoice
// is never the one invoice missing from every company's total: it must land
// in the registry, under the default company, the moment that invoice is
// stored — not sit "unassigned" until someone notices it.
func TestInsertInvoiceRegistersTheVehicleUnderTheDefaultCompany(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{InvoiceNumber: "INV-1", VehicleReg: "LL64BPY", Brutto: 93.66})

	v, err := db.GetVehicle("LL64BPY")
	if err != nil {
		t.Fatalf("GetVehicle: %v", err)
	}
	if v.CompanyID == nil {
		t.Fatalf("vehicle has no company at all — its cost still counts toward nothing")
	}
	if v.CompanyName != "Overall Clients" {
		t.Fatalf("company = %q, want Overall Clients", v.CompanyName)
	}

	companies, err := db.Companies()
	if err != nil {
		t.Fatalf("Companies: %v", err)
	}
	for _, c := range companies {
		if c.Name == "Overall Clients" && c.Brutto != 93.66 {
			t.Fatalf("Overall Clients brutto = %.2f, want 93.66 — the invoice is not being counted", c.Brutto)
		}
	}

	unassigned, err := db.UnassignedVehicles()
	if err != nil {
		t.Fatalf("UnassignedVehicles: %v", err)
	}
	if len(unassigned) != 0 {
		t.Fatalf("still unassigned: %+v — should have been auto-registered", unassigned)
	}
}

// A vehicle already assigned to a real company must never be bumped back to
// Overall Clients just because another invoice for it comes in.
func TestInsertInvoiceDoesNotReassignAnAlreadyRegisteredVehicle(t *testing.T) {
	db := open(t)
	companies, err := db.Companies()
	if err != nil {
		t.Fatalf("Companies: %v", err)
	}
	var goldstarID int64
	for _, c := range companies {
		if c.Name == "GOLDSTAR DIAMOND CARS" {
			goldstarID = c.ID
		}
	}
	if goldstarID == 0 {
		t.Fatalf("seed company GOLDSTAR DIAMOND CARS not found")
	}
	if err := db.SaveVehicle("YG14MKD", VehiclePatch{CompanyID: &goldstarID}); err != nil {
		t.Fatalf("SaveVehicle: %v", err)
	}

	add(t, db, Invoice{InvoiceNumber: "INV-2", VehicleReg: "YG14MKD", Brutto: 49.25})

	v, err := db.GetVehicle("YG14MKD")
	if err != nil {
		t.Fatalf("GetVehicle: %v", err)
	}
	if v.CompanyName != "GOLDSTAR DIAMOND CARS" {
		t.Fatalf("company = %q, want GOLDSTAR DIAMOND CARS — InsertInvoice must not reassign it", v.CompanyName)
	}
}

// General stock and any other invoice with no plate at all must not produce a
// registry row for an empty registration.
func TestInsertInvoiceWithNoVehicleRegRegistersNothing(t *testing.T) {
	db := open(t)
	add(t, db, Invoice{InvoiceNumber: "INV-3", IsGeneral: true, Brutto: 12.00})

	registry, err := db.RegisteredVehicles(0)
	if err != nil {
		t.Fatalf("RegisteredVehicles: %v", err)
	}
	if len(registry) != 0 {
		t.Fatalf("registry = %+v, want empty", registry)
	}
}

// backfillOrphanVehicles is what fixes plates that already had invoices
// before ensureVehicleRegistered existed — proven here by inserting a row
// directly, bypassing InsertInvoice, then reopening the same database file
// the way a server restart would.
func TestReopenBackfillsOrphanVehiclesFromExistingInvoices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.db.Exec(`
		INSERT INTO invoices (file_sha256, source_file, vehicle_reg, currency, brutto, created_at)
		VALUES ('orphan-sha', '', 'HJ72MHE', 'GBP', 83.16, datetime('now'))`); err != nil {
		t.Fatalf("seed orphan invoice: %v", err)
	}

	before, err := db.UnassignedVehicles()
	if err != nil {
		t.Fatalf("UnassignedVehicles: %v", err)
	}
	if len(before) != 1 || before[0].VehicleReg != "HJ72MHE" {
		t.Fatalf("unassigned = %+v, want just HJ72MHE — the test seeded the invoice directly, bypassing registration", before)
	}

	db.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	v, err := reopened.GetVehicle("HJ72MHE")
	if err != nil {
		t.Fatalf("GetVehicle after reopen: %v", err)
	}
	if v.CompanyName != "Overall Clients" {
		t.Fatalf("company = %q, want Overall Clients", v.CompanyName)
	}
}
