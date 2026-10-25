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

// ageOn returns the whole years that a member born on the given day, month and
// year has reached on the date (y, m, d). The birthday counts as reached when
// (m, d) is not before (month, day) in the calendar, so a member born on
// 29 February reaches the birthday on 1 March in a year that is not a leap year.
// It is nil when the year is not set or lies in the future.
func ageOn(day, month int, year *int, y int, m time.Month, d int) *int {
	if year == nil {
		return nil
	}
	age := y - *year
	if int(m) < month || (int(m) == month && d < day) {
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
// today on 28 February in a year that is not a leap year. The age is the age that
// [ageOn] gives, so on that 28 February it is still the age before the birthday.
//
// A viewer without account.view_profiles gets no rows and no error, because the
// names link to profiles that the viewer may not see.
func Birthdays(ctx context.Context, q db.Querier, v Viewer, now time.Time) ([]BirthdayRow, error) {
	if !v.Perms.Has("account.view_profiles", 0) {
		return nil, nil
	}
	y, m, d := calendarDay(v, now)
	// On 28 February of a year with no 29 February, the 29th is also today.
	leapDay := m == time.February && d == 28 && !isLeap(y)
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
