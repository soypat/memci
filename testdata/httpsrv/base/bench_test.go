package main

import "testing"

var sink string

func BenchmarkGreet(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = greet("/world")
	}
}

// BenchmarkStable is identical on both sides, so it must never appear in the
// report. It is the fixture's check that unchanged rows are dropped.
func BenchmarkStable(b *testing.B) {
	for i := 0; i < b.N; i++ {
		sink = string(make([]byte, 32))
	}
}
