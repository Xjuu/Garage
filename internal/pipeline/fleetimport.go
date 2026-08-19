package pipeline

import (
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strings"

	"goldstar/internal/store"
)

// makeSpelling canonicalises manufacturer names. A dispatch export is typed by
// many hands over years, so the same marque arrives as TOYOTA, TOYOTTA, TOTOTA
// and Toyota. Left alone they become four separate makes in every report.
//
// Only unambiguous misspellings are corrected, and every correction is reported
// rather than applied silently — this is the operator's data, not ours.
var makeSpelling = map[string]string{
	"TOYOTTA": "Toyota", "TOTOTA": "Toyota", "TOYOTA": "Toyota",
	"SKODA": "Skoda", "SKODAA": "Skoda",
	"VW": "VW", "VOLKSWAGEN": "VW",
	"MERC": "Mercedes", "MERCEDES": "Mercedes", "MERCEDEZ": "Mercedes",
	"MERCEDES BENZ": "Mercedes",
	"HYUNDAI":       "Hyundai", "HYNDAI": "Hyundai", "HYUNDI": "Hyundai",
	"PEUGEOT": "Peugeot", "PEAGUEOT": "Peugeot",
	"RENAULT": "Renault", "RENALT": "Renault",
	"FORD": "Ford", "SEAT": "SEAT", "VAUXHALL": "Vauxhall",
	"NISSAN": "Nissan", "KIA": "Kia", "HONDA": "Honda", "BMW": "BMW",
	"AUDI": "Audi", "LEXUS": "Lexus", "VOLVO": "Volvo", "DACIA": "Dacia",
	"SUZUKI": "Suzuki", "MITSUBISHI": "Mitsubishi", "LEVC": "LEVC",
	"LTI": "LTI", "LONDON TAXI": "London Taxi",
}

// placeholders are template rows that dispatch software leaves behind. They
// carry a plausible-looking registration and would otherwise import as real
// vehicles.
var placeholders = map[string]bool{"MAKE": true, "MODEL": true, "REG1": true, "ZZZ": true, "ZZZZ": true}

// FleetRow is one line of the export after cleaning.
type FleetRow struct {
	Callsigns []string
	Make      string
	Model     string
	Reg       string
}

// FleetReport describes what an import did, or would do on a dry run.
type FleetReport struct {
	Rows        int
	Vehicles    int
	Created     int
	Updated     int
	Rejected    []string
	Duplicates  []string
	Corrections []string
	Odd         []string
	// Unlisted are registrations already appearing on invoices that the fleet
	// export does not mention.
	Unlisted []string
	Applied  bool
}

// titleCase gives "PASSAT GTE" as "Passat GTE": words already mixed-case or
// short enough to be an abbreviation are left as the operator wrote them.
func titleCase(s string) string {
	fields := strings.Fields(strings.TrimSpace(s))
	for i, w := range fields {
		if len(w) <= 3 && w == strings.ToUpper(w) {
			continue // GTE, CHR, TX4 and similar
		}
		fields[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(fields, " ")
}

// ImportFleetCSV loads an Autocab-style vehicle export into the registry.
//
// Nothing is written unless apply is true, so the operator can see the effect
// first — a bulk write over a fleet registry is not something to discover
// after the fact.
// company, when non-empty, names the company every imported vehicle is
// assigned to. Left empty, existing assignments are preserved and new vehicles
// fall to the default company.
func ImportFleetCSV(db *store.Store, path, company string, apply bool, logf LogFunc) (*FleetReport, error) {
	var companyID *int64
	if strings.TrimSpace(company) != "" {
		companies, err := db.Companies()
		if err != nil {
			return nil, err
		}
		var names []string
		for i := range companies {
			names = append(names, companies[i].Name)
			if strings.EqualFold(companies[i].Name, company) {
				id := companies[i].ID
				companyID = &id
			}
		}
		if companyID == nil {
			return nil, fmt.Errorf("no company called %q; known: %s", company, strings.Join(names, ", "))
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("%s has no data rows", path)
	}

	// Locate the columns by name so a reordered export still works.
	head := records[0]
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, need := range []string{"callsign", "make", "model", "registration"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("column %q is missing; found %v", need, head)
		}
	}

	rep := &FleetReport{Rows: len(records) - 1, Applied: apply}
	byReg := map[string]*FleetRow{}
	corrected := map[string]string{}

	for _, rec := range records[1:] {
		get := func(name string) string {
			i := col[name]
			if i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		callsign, rawMake, rawModel, rawReg := get("callsign"), get("make"), get("model"), get("registration")

		// The store's own rule: a UK plate contains at least one digit, so
		// "JJJ", "faz" and "/........." are not registrations.
		reg := store.NormalizeReg(rawReg)
		if reg == "" {
			rep.Rejected = append(rep.Rejected,
				fmt.Sprintf("callsign %s: %q is not a registration", callsign, rawReg))
			continue
		}
		if placeholders[strings.ToUpper(rawMake)] || placeholders[strings.ToUpper(rawReg)] {
			rep.Rejected = append(rep.Rejected,
				fmt.Sprintf("callsign %s: template row (%s / %s)", callsign, rawMake, rawReg))
			continue
		}

		mk := titleCase(rawMake)
		if canon, ok := makeSpelling[strings.ToUpper(rawMake)]; ok {
			if canon != titleCase(rawMake) {
				corrected[strings.ToUpper(rawMake)] = canon
			}
			mk = canon
		}
		model := titleCase(rawModel)

		// A colour where a make belongs, or a make where a model belongs, is
		// worth surfacing rather than importing as though it were a spec.
		if isColour(rawMake) || isColour(rawModel) {
			rep.Odd = append(rep.Odd,
				fmt.Sprintf("%s (callsign %s): make/model look wrong — %q / %q", reg, callsign, rawMake, rawModel))
		}

		if existing, ok := byReg[reg]; ok {
			existing.Callsigns = append(existing.Callsigns, callsign)
			continue
		}
		byReg[reg] = &FleetRow{Callsigns: []string{callsign}, Make: mk, Model: model, Reg: reg}
	}

	regs := make([]string, 0, len(byReg))
	for reg := range byReg {
		regs = append(regs, reg)
	}
	sort.Strings(regs)
	rep.Vehicles = len(regs)

	for _, reg := range regs {
		row := byReg[reg]
		if len(row.Callsigns) > 1 {
			rep.Duplicates = append(rep.Duplicates,
				fmt.Sprintf("%s is listed under callsigns %s", reg, strings.Join(row.Callsigns, ", ")))
		}

		note := "Callsign " + strings.Join(row.Callsigns, ", ")

		// Preserve anything a human has already set. The export knows nothing
		// about drivers or which company a car belongs to, so importing must
		// not blank them.
		patch := store.VehiclePatch{Make: &row.Make, Model: &row.Model, Notes: &note}
		if companyID != nil {
			patch.CompanyID = companyID
		}
		if cur, err := db.GetVehicle(reg); err == nil && cur != nil {
			rep.Updated++
			if cur.Driver != "" {
				patch.Driver = &cur.Driver
			}
			if cur.Year != "" {
				patch.Year = &cur.Year
			}
			// An explicit --company wins; otherwise keep where the vehicle is.
			if companyID == nil && cur.CompanyID != nil {
				patch.CompanyID = cur.CompanyID
			}
			patch.Active = &cur.Active
			if cur.Notes != "" && !strings.HasPrefix(cur.Notes, "Callsign") {
				merged := cur.Notes + " · " + note
				patch.Notes = &merged
			}
		} else {
			rep.Created++
		}

		if apply {
			if err := db.SaveVehicle(reg, patch); err != nil {
				return rep, fmt.Errorf("save %s: %w", reg, err)
			}
		}
	}

	for from, to := range corrected {
		rep.Corrections = append(rep.Corrections, fmt.Sprintf("%s → %s", from, to))
	}
	sort.Strings(rep.Corrections)

	// A car that has been invoiced but is absent from the fleet export is the
	// most useful thing this whole exercise can surface: its costs are already
	// being recorded against a plate nobody can identify.
	if seen, err := db.Vehicles(); err == nil {
		for _, v := range seen {
			if v.VehicleReg == "" {
				continue
			}
			if _, inExport := byReg[store.NormalizeReg(v.VehicleReg)]; !inExport {
				rep.Unlisted = append(rep.Unlisted, fmt.Sprintf(
					"%s has %d invoice(s) totalling £%.2f but is not in the export",
					v.VehicleReg, v.Invoices, v.Brutto))
			}
		}
		sort.Strings(rep.Unlisted)
	}
	return rep, nil
}

var colours = map[string]bool{
	"BLUE": true, "GREY": true, "GRAY": true, "BLACK": true, "SILVER": true,
	"WHITE": true, "RED": true, "GREEN": true,
}

func isColour(s string) bool { return colours[strings.ToUpper(strings.TrimSpace(s))] }
