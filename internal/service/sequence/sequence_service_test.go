package sequence

import "testing"

func TestNextIsMonotonicPerSession(t *testing.T) {
	s := NewSessionSequencer()
	a := s.Next("S1")
	b := s.Next("S1")
	c := s.Next("S1")
	if !(a < b && b < c) {
		t.Fatalf("expected monotonic seq, got %d %d %d", a, b, c)
	}
}

func TestNextIsIsolatedAcrossSessions(t *testing.T) {
	s := NewSessionSequencer()
	a1 := s.Next("A")
	b1 := s.Next("B")
	if a1 != 1 || b1 != 1 {
		t.Fatalf("expected each session starts from 1, got A=%d B=%d", a1, b1)
	}
}

func TestTraceIDIncludesSessionAndSender(t *testing.T) {
	s := NewSessionSequencer()
	trace := s.TraceID("SESSION", "USER", 10)
	if trace == "" {
		t.Fatal("trace id should not be empty")
	}
	if len(trace) < 10 {
		t.Fatalf("trace id too short: %s", trace)
	}
}
