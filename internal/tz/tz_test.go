package tz

import (
	"slices"
	"testing"
	"time"
)

func TestZonesLoad(t *testing.T) {
	if n := len(Zones); n < 400 || n > 450 {
		t.Errorf("len(Zones) = %d, want 400 to 450", n)
	}
	if !slices.IsSorted(Zones) {
		t.Error("Zones is not sorted")
	}
	if got := len(slices.Compact(slices.Clone(Zones))); got != len(Zones) {
		t.Errorf("Zones holds %d names once, want %d: it has a duplicate", got, len(Zones))
	}
	for _, name := range Zones {
		if _, err := time.LoadLocation(name); err != nil {
			t.Errorf("Zones: LoadLocation(%q): %v", name, err)
		}
	}
	for _, c := range Common {
		if _, err := time.LoadLocation(c.Name); err != nil {
			t.Errorf("Common: LoadLocation(%q): %v", c.Name, err)
		}
		if !Known(c.Name) {
			t.Errorf("Common name %q is not in Zones", c.Name)
		}
		if c.Label == "" {
			t.Errorf("Common name %q has no label", c.Name)
		}
	}
}

func TestCommonOrder(t *testing.T) {
	want := []Choice{
		{"America/New_York", "Eastern: New York"},
		{"America/Chicago", "Central: Chicago"},
		{"America/Denver", "Mountain: Denver"},
		{"America/Phoenix", "Mountain, no DST: Phoenix"},
		{"America/Los_Angeles", "Pacific: Los Angeles"},
		{"America/Anchorage", "Alaska: Anchorage"},
		{"Pacific/Honolulu", "Hawaii: Honolulu"},
		{"America/Halifax", "Atlantic: Halifax"},
		{"Europe/London", "London"},
		{"Europe/Berlin", "Central Europe: Berlin"},
	}
	if !slices.Equal(Common, want) {
		t.Errorf("Common = %v, want %v", Common, want)
	}
}

func TestKnown(t *testing.T) {
	for _, name := range []string{"America/New_York", "Europe/Kyiv", "Pacific/Funafuti"} {
		if !Known(name) {
			t.Errorf("Known(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"Etc/GMT+4", "UTC", "Etc/UTC", "GMT", "EST5EDT", "Local", "Australia/ACT", "",
		"america/new_york", " America/New_York",
	} {
		if Known(name) {
			t.Errorf("Known(%q) = true, want false", name)
		}
	}
}
