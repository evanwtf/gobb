package metrics

// The metrics that the packages of gobb update. Each one lives in [Default].
// The gauges that read the database, the pool or the memory gate are registered
// by `gobb serve`, which holds those values.

// httpBuckets are the upper bounds of the request durations, 5 ms to 10 s.
var httpBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

var (
	// HTTPRequests counts the answers of the web server. route is the pattern
	// that matched, without its method ("/t/{id}"), or "none". method is GET,
	// HEAD, POST or other. code is the status class, "2xx" to "5xx".
	HTTPRequests = Default.CounterVec("gobb_http_requests_total",
		"HTTP requests answered, by route pattern, method and status class.", "route", "method", "code")
	// HTTPDuration is the time from the start of a request to the end of its
	// handler, by route pattern.
	HTTPDuration = Default.HistogramVec("gobb_http_request_duration_seconds",
		"Time to answer an HTTP request, by route pattern.", httpBuckets, "route")
	// SigninAttempts counts the sign-in checks by result: ok, wrong (a wrong name
	// or password, or an account that changed during the check), ambiguous (an
	// email address of several accounts), limited, banned, inactive, busy (the
	// memory gate stayed full) and error.
	SigninAttempts = Default.CounterVec("gobb_signin_attempts_total",
		"Sign-in checks, by result.", "result")
	// RateLimited counts the checks that found a limit full, by the name of the
	// limit (ratelimit.Limiter.Named).
	RateLimited = Default.CounterVec("gobb_rate_limited_total",
		"Checks that found a rate limit full, by limit.", "limit")
	// Emails counts the sends of the email queue by result: sent, failed (to be
	// tried again) and dropped.
	Emails = Default.CounterVec("gobb_emails_total",
		"Emails that the queue sent, failed to send or dropped.", "result")
	// ResetSkipped counts the accounts that a reset request reached and that got
	// no email, by reason: board_hour and board_day (the limits of the board),
	// account_day (the limit of the account), admin, and ineligible (inactive,
	// banned, no usable address, or no right to change the password).
	ResetSkipped = Default.CounterVec("gobb_reset_emails_skipped_total",
		"Password reset emails not sent, by reason.", "reason")
	// SigninFloor is the least time that a failed sign-in takes.
	SigninFloor = Default.Gauge("gobb_signin_floor_seconds",
		"The least time that a failed sign-in takes.")
)

func init() {
	for _, r := range []string{"ok", "wrong", "ambiguous", "limited", "banned", "inactive", "busy", "error"} {
		SigninAttempts.Init(r)
	}
	for _, r := range []string{"sent", "failed", "dropped"} {
		Emails.Init(r)
	}
	for _, r := range []string{"board_hour", "board_day", "account_day", "admin", "ineligible"} {
		ResetSkipped.Init(r)
	}
}

// StatusClass returns the class of an HTTP status code: "1xx" to "5xx", or
// "other" for a code outside 100 to 599.
func StatusClass(code int) string {
	if code < 100 || code > 599 {
		return "other"
	}
	return string(rune('0'+code/100)) + "xx"
}

// Method returns m when it is GET, HEAD or POST, and "other" for any other
// method, so the method label stays bounded.
func Method(m string) string {
	switch m {
	case "GET", "HEAD", "POST":
		return m
	}
	return "other"
}
