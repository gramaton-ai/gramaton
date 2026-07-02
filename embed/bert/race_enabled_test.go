//go:build race

package bert

// raceDetectorEnabled reports whether this test binary was built with
// -race. The embedding speedup gates assert throughput ratios that
// the race detector's instrumentation distorts (parallel inference
// loses its advantage under the added synchronization), so the gate
// tests skip themselves when this is true. Correctness-focused
// concurrency tests in this package must NOT consult this constant.
const raceDetectorEnabled = true
