// Package tz holds the list of time zones that gobb accepts: the places of the IANA
// zone.tab file. A zone is a place, such as America/New_York. gobb offers no fixed
// offset (Etc/GMT+4) and no bare UTC, so a member's time follows the rules of a
// real place, daylight saving time included.
//
// The list lives in zones.go, which scripts/zonetab generates.
package tz

import (
	"slices"
	_ "time/tzdata" // zone data for a host that has none of its own
)

// Choice is a zone that the settings page offers in its "Common" group, with the
// label that the page shows.
type Choice struct {
	Name  string // zone name, a member of [Zones]
	Label string
}

// Common lists the zones that the settings page offers above the full list, in
// the order it shows them.
var Common = []Choice{
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

// Known reports whether name is in [Zones]. The match is exact: it ignores
// neither case nor white space.
func Known(name string) bool {
	_, ok := slices.BinarySearch(Zones, name)
	return ok
}
