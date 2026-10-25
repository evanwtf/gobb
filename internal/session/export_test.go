package session

// Create starts a session for userID with no check. Production code starts a
// session with CreateChecked only; the tests of this package need a session for a
// user in any state.
var Create = create
