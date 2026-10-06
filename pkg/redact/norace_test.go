//go:build !race

package redact

// raceDetector is true in a build with the race detector, which makes this
// package's code about twenty times slower.
const raceDetector = false
