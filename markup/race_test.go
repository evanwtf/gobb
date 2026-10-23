//go:build race

package markup

// raceSlowdown scales time limits: the race detector slows the parser a lot.
const raceSlowdown = 10
