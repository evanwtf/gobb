package board

import (
	"context"
	"fmt"
	"time"

	"github.com/evanwtf/gobb/internal/db"
)

// calendarDay returns the date of now on the calendar of the viewer's zone. A
// viewer with no zone gets UTC.
func calendarDay(v Viewer, now time.Time) (year int, month time.Month, day int) {
	loc := v.Loc
	if loc == nil {
		loc = time.UTC
	}
	return now.In(loc).Date()
}

// birthdayIn returns the day of the month on which a birthday on the given day
// and month falls in year y. A 29 February birthday falls on 28 February in a
// year that is not a leap year. [ageOn] and [Birthdays] both use it, so the age
// and the list agree.
func birthdayIn(day, month, y int) int {
	if month == 2 && day == 29 && !isLeap(y) {
		return 28
	}
	return day
}

// ageOn returns the whole years that a member born on the given day, month and
// year has reached on the date (y, m, d). The birthday counts as reached when
// (m, d) is not before the day that [birthdayIn] gives. It is nil when the year
// is not set or lies in the future.
func ageOn(day, month int, year *int, y int, m time.Month, d int) *int {
	if year == nil {
		return nil
	}
	age := y - *year
	if int(m) < month || (int(m) == month && d < birthdayIn(day, month, y)) {
		age--
	}
	if age < 0 {
		return nil
	}
	return &age
}

// isLeap reports whether year y has a 29 February.
func isLeap(y int) bool {
	return y%4 == 0 && (y%100 != 0 || y%400 == 0)
}

// BirthdayRow is one member who has a birthday today.
type BirthdayRow struct {
	ID   int64
	Name string
	// Age is nil when the member gave no year.
	Age *int
}

// Birthdays returns the active members whose birthday is today, on the calendar
// date of now in the zone of v, in name order. A 29 February birthday counts as
// today on 28 February in a year that is not a leap year ([birthdayIn]), and the
// age from [ageOn] counts that day as the birthday.
//
// A viewer without account.view_profiles gets no rows and no error, because the
// names link to profiles that the viewer may not see.
func Birthdays(ctx context.Context, q db.Querier, v Viewer, now time.Time) ([]BirthdayRow, error) {
	if !v.Perms.Has("account.view_profiles", 0) {
		return nil, nil
	}
	y, m, d := calendarDay(v, now)
	// A 29 February birthday is also today when [birthdayIn] puts it on d.
	leapDay := m == time.February && d != 29 && birthdayIn(29, 2, y) == d
	rows, err := q.Query(ctx, `
select id, username, birth_day, birth_month, birth_year
  from users
 where state = 'active' and birth_month is not null
   and ((birth_month = $1 and birth_day = $2) or ($3 and birth_month = 2 and birth_day = 29))
 order by username_clean, id`, int(m), d, leapDay)
	if err != nil {
		return nil, fmt.Errorf("board: list birthdays: %w", err)
	}
	defer rows.Close()
	var out []BirthdayRow
	for rows.Next() {
		var (
			r          BirthdayRow
			day, month int
			year       *int
		)
		if err := rows.Scan(&r.ID, &r.Name, &day, &month, &year); err != nil {
			return nil, fmt.Errorf("board: list birthdays: %w", err)
		}
		r.Age = ageOn(day, month, year, y, m, d)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: list birthdays: %w", err)
	}
	return out, nil
}
