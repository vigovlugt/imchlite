package exiftool

import "testing"

func TestParseExifDate(t *testing.T) {
	cases := []struct {
		value string
		want  string
		ok    bool
	}{
		{"2023:10:06 06:39:09", "2023-10-06T06:39:09Z", true},
		{" 2023:10:06 06:39:09 ", "2023-10-06T06:39:09Z", true},
		{"2023:10:06 06:39:09.123456", "2023-10-06T06:39:09Z", true},
		{"2023:10:06 06:39:09,5", "2023-10-06T06:39:09Z", true},
		{"not a date", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := parseExifDate(c.value)
		if ok != c.ok {
			t.Errorf("parseExifDate(%q) ok = %v, want %v", c.value, ok, c.ok)
			continue
		}
		if ok && got.UTC().Format("2006-01-02T15:04:05Z") != c.want {
			t.Errorf("parseExifDate(%q) = %s, want %s", c.value, got, c.want)
		}
	}
}

func TestParseZonedDate(t *testing.T) {
	cases := []struct {
		value      string
		wantWall   string
		wantOffset string
	}{
		{"2023-10-06T08:39:09+0200", "2023-10-06T08:39:09", "+02:00"},
		{"2023-10-06T08:39:09+02:00", "2023-10-06T08:39:09", "+02:00"},
		{"2023:10:06 08:39:09-04:00", "2023-10-06T08:39:09", "-04:00"},
		{"2015-03-22T10:22:56Z", "2015-03-22T10:22:56", "+00:00"},
	}
	for _, c := range cases {
		got, offset, ok := parseZonedDate(c.value)
		if !ok {
			t.Errorf("parseZonedDate(%q) failed", c.value)
			continue
		}
		if got.Format("2006-01-02T15:04:05") != c.wantWall {
			t.Errorf("parseZonedDate(%q) wall = %s, want %s", c.value, got, c.wantWall)
		}
		if formatZoneOffset(offset) != c.wantOffset {
			t.Errorf("parseZonedDate(%q) offset = %s, want %s", c.value, formatZoneOffset(offset), c.wantOffset)
		}
	}

	if _, _, ok := parseZonedDate("2023:10:06 08:39:09"); ok {
		t.Error("parseZonedDate accepted a naive date")
	}
}

func TestParseGPSDate(t *testing.T) {
	if got, ok := parseGPSDate("2022:11:11 19:23:54Z"); !ok || got.Format("2006-01-02T15:04:05Z") != "2022-11-11T19:23:54Z" {
		t.Errorf("parseGPSDate = %v, %v", got, ok)
	}
	if got, ok := parseGPSDate("2022:11:11 19:23:54"); !ok || got.Format("2006-01-02T15:04:05Z") != "2022-11-11T19:23:54Z" {
		t.Errorf("parseGPSDate = %v, %v", got, ok)
	}
}
