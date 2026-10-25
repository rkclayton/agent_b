//go:build race

package broker

// raceDetector is true under -race, which slows the code several times over (item 2wp).
const raceDetector = true
