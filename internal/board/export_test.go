package board

// SetParseHook sets the hook that ParseBody runs just before the parse, and
// returns the function that puts the old one back. A test uses it to hold a parse
// open and to see what the database holds meanwhile.
func SetParseHook(f func()) (restore func()) {
	old := parseHook
	parseHook = f
	return func() { parseHook = old }
}
