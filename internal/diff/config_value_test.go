package diff

import "testing"

func TestConfigValueSQLKeepsCase(t *testing.T) {
	for in, want := range map[string]string{
		"UTC":               "'UTC'",
		"Prod":              "'Prod'",
		"utc":               "utc",
		"on":                "on",
		"64MB":              "'64MB'",
		"30":                "30",
		"America/New_York":  "'America/New_York'",
		`"$user", public`:   `"$user", public`,
		`"MySchema", public`: `"MySchema", public`,
		"'ISO, MDY'":        "'ISO, MDY'",
	} {
		if got := configValueSQL(in); got != want {
			t.Errorf("configValueSQL(%q) = %s, want %s", in, got, want)
		}
	}
}
