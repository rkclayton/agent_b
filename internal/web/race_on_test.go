//go:build race

package web

// raceDetector is true under -race, which slows the scheduler several times over (item 2wz).
const raceDetector = true
