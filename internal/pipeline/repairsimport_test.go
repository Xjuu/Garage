package pipeline

import "testing"

func TestSplitMakeModel(t *testing.T) {
	cases := []struct{ in, mk, model string }{
		{"FORD TRANSIT", "Ford", "Transit"},
		{"SKODA OCTAVIA", "Skoda", "Octavia"},
		{"VOLKSWAGEN T-PORTER", "Volkswagen", "T-Porter"},
		{"MERCEDES BENZ VITO", "Mercedes Benz", "Vito"},
		{"MERCEDES VITO", "Mercedes", "Vito"}, // only 2 words here — no special-case needed
		{"FORD", "Ford", ""},                  // make with no model at all in the source
		{"", "", ""},
	}
	for _, c := range cases {
		mk, model := splitMakeModel(c.in)
		if mk != c.mk || model != c.model {
			t.Errorf("splitMakeModel(%q) = %q, %q; want %q, %q", c.in, mk, model, c.mk, c.model)
		}
	}
}

func TestMatchSpecLabel(t *testing.T) {
	cases := []struct{ label, want string }{
		{"Registration", "registration"},
		{"Vin/Chassis number ", "vin"},
		{"Make and model ", "make_model"},
		{"Colour", "colour"},
		{"Cylinder capacity", "cylinder_capacity"},
		{"Spare key/Number of keys", "spare_keys"},
		{"Type of fuel ", "fuel_type"},
		{"Engine Number ", "engine_number"},
		{"Tyre Size ", "tyre_size"},
		{"Radio code", "radio_code"},
		{"OIL", "oil_amount"},
		{"0IL", "oil_amount"}, // the real typo in the source file
		{"Oil", "oil_amount"},
		{"Timing belt change ", ""}, // the belt sub-table's own header, not a spec row
		{"", ""},
	}
	for _, c := range cases {
		if got := matchSpecLabel(c.label); got != c.want {
			t.Errorf("matchSpecLabel(%q) = %q, want %q", c.label, got, c.want)
		}
	}
}

func TestParseMileage(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"28634", 28634},
		{"117741 KM", 117741},
		{"238899KM", 238899},
		{"217164km", 217164},
		{"", 0},
		{"KM", 0},
	}
	for _, c := range cases {
		if got := parseMileage(c.in); got != c.want {
			t.Errorf("parseMileage(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseServiceDate(t *testing.T) {
	got, ok := parseServiceDate("10-18-19")
	if !ok || got != "2019-10-18" {
		t.Fatalf("parseServiceDate(10-18-19) = %q, %v; want 2019-10-18, true", got, ok)
	}
	// The whole point of MM-DD-YY: a day-of-month over 12 proves it can only
	// be MM-DD, never DD-MM — pin that down explicitly.
	got, ok = parseServiceDate("05-18-21")
	if !ok || got != "2021-05-18" {
		t.Fatalf("parseServiceDate(05-18-21) = %q, %v; want 2021-05-18, true", got, ok)
	}
	// Variable-width, slash-separated — a real form seen in the source file
	// alongside the more common zero-padded hyphenated one.
	got, ok = parseServiceDate("1/4/24")
	if !ok || got != "2024-01-04" {
		t.Fatalf("parseServiceDate(1/4/24) = %q, %v; want 2024-01-04, true", got, ok)
	}
	if _, ok := parseServiceDate(""); ok {
		t.Fatalf("an empty date should not parse")
	}
	if _, ok := parseServiceDate("not a date"); ok {
		t.Fatalf("garbage should not parse")
	}
}

func TestClassifyServiceType(t *testing.T) {
	cases := []struct {
		raw, wantType, wantOther string
		wantNote                 bool
	}{
		{"FULL SERVICE", "full", "", false},
		{"MINI SERVICE", "mini", "", false},
		{"FUII SERVICE", "full", "", true},    // known typo, still classified, original text kept
		{"MNI SERVICE", "mini", "", true},     // known typo
		{"FULL SERVICE BY MERCEDES", "full", "", true},
		{"EGR VALVE", "other", "EGR VALVE", false},
		{"GEARBOX SERVICE", "other", "GEARBOX SERVICE", false},
	}
	for _, c := range cases {
		st, other, note := classifyServiceType(c.raw)
		if st != c.wantType || other != c.wantOther {
			t.Errorf("classifyServiceType(%q) = %q, %q; want %q, %q", c.raw, st, other, c.wantType, c.wantOther)
		}
		if (note != "") != c.wantNote {
			t.Errorf("classifyServiceType(%q) note = %q, wantNote=%v", c.raw, note, c.wantNote)
		}
	}
}
