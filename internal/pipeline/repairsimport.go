package pipeline

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"goldstar/internal/store"
)

// RepairsReport describes what a historical workshop spreadsheet import did,
// or would do on a dry run.
type RepairsReport struct {
	Sheets        int
	SheetsSkipped int
	Vehicles      int
	MainRows      int
	BeltRows      int
	Imported      int
	// Skipped lists every row that could not be imported, and why — a
	// count alone would leave the operator no way to find and fix the gap.
	Skipped []string
	Applied bool
}

// multiWordMakes are the manufacturer names that would otherwise be split
// wrong by "first word is the make, the rest is the model" — everything
// else in this sheet (FORD TRANSIT, SKODA OCTAVIA, VOLKSWAGEN T-PORTER...)
// splits correctly on the first space.
var multiWordMakes = []string{"MERCEDES BENZ", "LAND ROVER", "ALFA ROMEO", "ASTON MARTIN", "GREAT WALL"}

func splitMakeModel(s string) (mk, model string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	upper := strings.ToUpper(s)
	for _, m := range multiWordMakes {
		if strings.HasPrefix(upper, m) {
			return titleCase(m), titleCase(strings.TrimSpace(s[len(m):]))
		}
	}
	fields := strings.Fields(s)
	if len(fields) == 1 {
		return titleCase(fields[0]), ""
	}
	return titleCase(fields[0]), titleCase(strings.Join(fields[1:], " "))
}

// matchSpecLabel maps one of the sheet's header-block row labels to the
// VehicleSpecPatch field it fills, tolerating the trailing spaces and
// inconsistent capitalisation years of hand typing produced. "" means the
// row is not a spec field at all (blank rows, the record title).
func matchSpecLabel(label string) string {
	u := strings.ToUpper(strings.TrimSpace(label))
	switch {
	case u == "REGISTRATION":
		return "registration"
	case strings.Contains(u, "VIN") || strings.Contains(u, "CHASSIS"):
		return "vin"
	case strings.Contains(u, "MAKE"):
		return "make_model"
	case strings.Contains(u, "COLOUR") || strings.Contains(u, "COLOR"):
		return "colour"
	case strings.Contains(u, "CYLINDER"):
		return "cylinder_capacity"
	case strings.Contains(u, "SPARE KEY") || strings.Contains(u, "NUMBER OF KEYS"):
		return "spare_keys"
	case strings.Contains(u, "FUEL"):
		return "fuel_type"
	case strings.Contains(u, "ENGINE NUMBER"):
		return "engine_number"
	case strings.Contains(u, "TYRE"):
		return "tyre_size"
	case strings.Contains(u, "RADIO"):
		return "radio_code"
	case u == "OIL" || u == "0IL": // "0IL" is a real typo in the source file
		return "oil_amount"
	}
	return ""
}

var digitsRe = regexp.MustCompile(`[0-9]+`)

// parseMileage strips everything but digits ("117741 KM", "238899KM",
// "217164km" all read as printed) so the unit suffix, wherever it shows up,
// never breaks the number.
func parseMileage(s string) float64 {
	digits := strings.Join(digitsRe.FindAllString(s, -1), "")
	if digits == "" {
		return 0
	}
	n, _ := strconv.ParseFloat(digits, 64)
	return n
}

// parseServiceDate reads the sheet's MM-DD-YY dates. Confirmed unambiguous
// by inspection: values like "10-18-19" cannot be DD-MM-YY (there is no
// 18th month), so every date in the source file is genuinely US-ordered.
func parseServiceDate(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	for _, layout := range []string{"01-02-06", "1-2-06", "01/02/06", "1/2/06", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

// Typos of "FULL SERVICE" / "MINI SERVICE" seen often enough in the actual
// file to correct by name rather than leave stranded in "other" — each one
// checked against the real vocabulary before being added here.
var fullServiceTypos = map[string]bool{
	"FUII SERVICE": true, "FULL SEVICE": true, "FULL SEVRICE": true, "FULLSERVICE": true,
}
var miniServiceTypos = map[string]bool{
	"MINE SERVICE": true, "MINI SERICE": true, "MINI SEVICE": true, "MNI SERVICE": true,
}

// classifyServiceType buckets the sheet's free-text "type of work" into
// full/mini/other. Anything beyond a bare "FULL SERVICE"/"MINI SERVICE" —
// a typo, or a qualifier like "FULL SERVICE BY MERCEDES" — keeps the
// original wording in note, so normalising the type never silently drops
// what was actually written.
func classifyServiceType(raw string) (serviceType, other, note string) {
	trimmed := strings.TrimSpace(raw)
	u := strings.ToUpper(trimmed)
	switch {
	case strings.Contains(u, "FULL") || fullServiceTypos[u]:
		serviceType = "full"
	case strings.Contains(u, "MINI") || miniServiceTypos[u]:
		serviceType = "mini"
	default:
		return "other", trimmed, ""
	}
	if u != "FULL SERVICE" && u != "MINI SERVICE" {
		note = "Imported record: " + trimmed
	}
	return serviceType, "", note
}

// ImportRepairsXLSX loads a "Goldstar Service Record"-style workbook — one
// sheet per vehicle, a spec header block followed by a service-history
// table and a separate timing-belt-change table — into the repairs log.
//
// Nothing is written unless apply is true. The two history tables are
// independent lists that only happen to render in the same spreadsheet
// rows (confirmed by inspection: their dates don't correspond row for
// row), so each becomes its own repairs entry rather than being paired up.
func ImportRepairsXLSX(db *store.Store, path string, apply bool, logf LogFunc) (*RepairsReport, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	rep := &RepairsReport{Applied: apply}

	for _, name := range f.GetSheetList() {
		rows, err := f.GetRows(name)
		if err != nil {
			return nil, fmt.Errorf("read sheet %q: %w", name, err)
		}
		rep.Sheets++

		if len(rows) < 3 || len(rows[2]) < 2 ||
			strings.TrimSpace(rows[2][0]) != "Registration" || strings.TrimSpace(rows[2][1]) == "" {
			rep.SheetsSkipped++ // a blank template sheet, not a real vehicle
			continue
		}
		reg := store.NormalizeReg(rows[2][1])
		if reg == "" {
			rep.SheetsSkipped++
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("sheet %q: %q is not a usable registration", name, rows[2][1]))
			continue
		}
		rep.Vehicles++
		logf.printf("%s: reading sheet %q", reg, name)

		var spec store.VehicleSpecPatch
		var oilAmount string
		headerRow := -1
		for i := 2; i < len(rows); i++ {
			r := rows[i]
			if len(r) == 0 {
				continue
			}
			if strings.TrimSpace(r[0]) == "Date" {
				headerRow = i
				break
			}
			label := matchSpecLabel(r[0])
			if label == "" || len(r) < 2 {
				continue
			}
			value := strings.TrimSpace(strings.Join(r[1:], " "))
			switch label {
			case "vin":
				spec.VIN = value
			case "make_model":
				spec.Make, spec.Model = splitMakeModel(value)
			case "colour":
				spec.Colour = value
			case "cylinder_capacity":
				spec.CylinderCapacity = value
			case "spare_keys":
				spec.SpareKeys = value
			case "fuel_type":
				spec.FuelType = value
			case "engine_number":
				spec.EngineNumber = value
			case "tyre_size":
				spec.TyreSize = value
			case "radio_code":
				spec.RadioCode = value
			case "oil_amount":
				oilAmount = value
			}
		}

		// Registered from the spec block alone, unconditionally — a vehicle
		// with no service history yet (a recent addition to the fleet) still
		// has a real spec worth keeping, and would otherwise vanish from the
		// registry entirely just because it has nothing in the table below.
		if apply {
			if err := db.UpdateVehicleSpec(reg, spec); err != nil {
				return nil, fmt.Errorf("%s: update vehicle spec: %w", reg, err)
			}
		}

		if headerRow < 0 {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: no service-history table found on this sheet", reg))
			continue
		}

		base := store.Repair{
			VehicleReg: reg, VIN: spec.VIN, Make: spec.Make, Model: spec.Model, Colour: spec.Colour,
			CylinderCapacity: spec.CylinderCapacity, SpareKeys: spec.SpareKeys, FuelType: spec.FuelType,
			EngineNumber: spec.EngineNumber, TyreSize: spec.TyreSize, RadioCode: spec.RadioCode,
			OilAmount: oilAmount,
		}

		for i := headerRow + 1; i < len(rows); i++ {
			r := rows[i]
			get := func(idx int) string {
				if idx < len(r) {
					return strings.TrimSpace(r[idx])
				}
				return ""
			}

			if date, typ, mileage := get(0), get(1), get(2); date != "" || typ != "" || mileage != "" {
				rep.MainRows++
				switch {
				case date == "":
					rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s row %d: no date (type=%q, mileage=%q)", reg, i+1, typ, mileage))
				case typ == "":
					rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s row %d: no service type (date=%q)", reg, i+1, date))
				default:
					parsed, ok := parseServiceDate(date)
					if !ok {
						rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s row %d: unreadable date %q", reg, i+1, date))
						break
					}
					st, other, note := classifyServiceType(typ)
					rec := base
					rec.ServiceDate, rec.ServiceType, rec.ServiceTypeOther = parsed, st, other
					rec.Mileage, rec.Description = parseMileage(mileage), note
					if apply {
						if _, err := db.LogRepair(rec, "historical-import"); err != nil {
							return nil, fmt.Errorf("%s row %d: %w", reg, i+1, err)
						}
					}
					rep.Imported++
				}
			}

			if beltDate, beltMileage := get(4), get(5); beltDate != "" || beltMileage != "" {
				rep.BeltRows++
				parsed, ok := parseServiceDate(beltDate)
				if !ok {
					rep.Skipped = append(rep.Skipped,
						fmt.Sprintf("%s row %d: timing belt entry with an unreadable date %q (mileage=%q)", reg, i+1, beltDate, beltMileage))
					continue
				}
				rec := base
				rec.ServiceDate = parsed
				rec.ServiceType, rec.ServiceTypeOther = "other", "Timing belt change"
				rec.TimingBeltChanged = true
				rec.Mileage = parseMileage(beltMileage)
				rec.Description = "Imported record: timing belt change"
				if apply {
					if _, err := db.LogRepair(rec, "historical-import"); err != nil {
						return nil, fmt.Errorf("%s row %d (timing belt): %w", reg, i+1, err)
					}
				}
				rep.Imported++
			}
		}
	}

	return rep, nil
}
