package web

import (
	"html/template"
	"strconv"
	"time"
)

// The display layouts of times and dates, in the date formats of a member.
const (
	timeLayout    = "Jan 2, 2006, 3:04 pm"
	dateLayout    = "Jan 2, 2006"
	isoTimeLayout = "2006-01-02T15:04:05-07:00"
	isoDateLayout = "2006-01-02"
)

// clock says how a page shows a time: in which zone, in which date format, and
// against which "now" an age is counted. A zero clock shows the standard format
// in UTC.
type clock struct {
	Loc *time.Location
	// Format is "standard", "relative" or "iso". Any other value is "standard".
	Format string
	// Now is the time of the request. Only the relative format reads it.
	Now time.Time
}

// timeTag writes t as a <time> element in the format of c. The datetime
// attribute holds the RFC 3339 time in UTC. A date shows the day and no hour.
// The relative format puts the standard text in the title attribute.
//
// The result is built from numbers, a layout and the fixed words of relText, so
// it holds no text of a request or of the database.
func timeTag(t time.Time, c clock, dateOnly bool) template.HTML {
	loc := c.Loc
	if loc == nil {
		loc = time.UTC
	}
	std, iso := timeLayout, isoTimeLayout
	if dateOnly {
		std, iso = dateLayout, isoDateLayout
	}
	local := t.In(loc)
	open := `<time datetime="` + t.UTC().Format(time.RFC3339) + `"`
	switch c.Format {
	case "iso":
		return template.HTML(open + `>` + local.Format(iso) + `</time>`)
	case "relative":
		return template.HTML(open + ` title="` + local.Format(std) + `">` + relText(t, c.Now, loc, std) + `</time>`)
	}
	return template.HTML(open + `>` + local.Format(std) + `</time>`)
}

// relText says how long before now t was: "just now", "N seconds ago" and so on
// up to "N years ago". Ages under 180 days count elapsed time, so a change of
// daylight saving time does not shift them. Older ages count whole calendar
// months and years in loc. A time more than a minute after now has no age; it
// gets the standard text in the layout std, so a date stays a date.
//
// N is never 1 in any band, so the words need no singular form.
func relText(t, now time.Time, loc *time.Location, std string) string {
	d := now.Sub(t)
	switch {
	case d < -time.Minute:
		return t.In(loc).Format(std)
	case d < 10*time.Second:
		return "just now"
	case d < 2*time.Minute:
		return agoText(int(d/time.Second), "seconds")
	case d < 2*time.Hour:
		return agoText(int(d/time.Minute), "minutes")
	case d < 48*time.Hour:
		return agoText(int(d/time.Hour), "hours")
	case d < 180*24*time.Hour:
		return agoText(int(d/(24*time.Hour)), "days")
	}
	months := wholeMonths(t.In(loc), now.In(loc))
	if months < 24 {
		return agoText(months, "months")
	}
	return agoText(months/12, "years")
}

// agoText writes an age as "N unit ago".
func agoText(n int, unit string) string { return strconv.Itoa(n) + " " + unit + " ago" }

// wholeMonths counts the calendar months from t to now, which must be in one
// zone. A month is whole when its day of the month and its time of day have come.
// The rule is plain and does not stretch at the end of a month: from 31 August, the
// sixth month is whole on 1 March and not on 28 February, although 28 February is
// the last day of its month. So 31 August to 28 February is 5 months.
func wholeMonths(t, now time.Time) int {
	m := (now.Year()-t.Year())*12 + int(now.Month()) - int(t.Month())
	rest := func(x time.Time) time.Duration {
		return time.Duration(x.Day())*24*time.Hour + time.Duration(x.Hour())*time.Hour +
			time.Duration(x.Minute())*time.Minute + time.Duration(x.Second())*time.Second +
			time.Duration(x.Nanosecond())
	}
	if rest(now) < rest(t) {
		m--
	}
	return m
}
