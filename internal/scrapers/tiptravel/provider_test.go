package tiptravel

import "testing"

func TestParsePrice(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"1.234,56", 1234.56},
		{"12.345,00", 12345.00},
		{"1,234.56", 1234.56},
		{"1234.56", 1234.56},
		{"USD 1.234,56", 0}, // not the parser's job (regex extracts numeric substring)
		{"", 0},
	}
	for _, c := range cases {
		got, _ := parsePrice(c.in)
		if got != c.want {
			t.Errorf("parsePrice(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPriceRegex(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"USD 1.234,56", "1.234,56"},
		{"US$ 12.345,00", "12.345,00"},
		{"EUR 999,99", "999,99"},
		{"nothing here", ""},
	}
	for _, c := range cases {
		m := priceRegex.FindStringSubmatch(c.in)
		got := ""
		if len(m) >= 2 {
			got = m[1]
		}
		if got != c.want {
			t.Errorf("priceRegex(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	// Make sure default values are stable and the function does not panic.
	cfg := LoadConfig()
	if cfg == nil {
		t.Fatal("LoadConfig returned nil")
	}
	if cfg.LoginURL == "" {
		t.Error("LoginURL should default to https://www.tiptravelya.com/")
	}
	if cfg.Username == "" {
		t.Error("Username should default to 'matriz' when env vars are empty")
	}
}