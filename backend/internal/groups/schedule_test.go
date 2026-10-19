package groups

import (
	"math"
	"testing"
)

func TestClipUTF8KeepsTheNewestBytes(t *testing.T) {
	text := stringsRepeat("a", 8190) + "é" + "Z"
	got := clipUTF8(text, 8192)
	if len(got) > 8192 {
		t.Fatalf("len=%d", len(got))
	}
	if got[len(got)-1] != 'Z' {
		t.Fatalf("dropped the newest byte: %q", got[len(got)-8:])
	}
	if !utf8Valid(got) {
		t.Fatal("cut inside a rune")
	}
}

func TestInt32CounterRejectsOutOfRange(t *testing.T) {
	if _, err := int32Counter(-1); err == nil {
		t.Fatal("accepted a negative counter")
	}
	if _, err := int32Counter(math.MaxInt32 + 1); err == nil {
		t.Fatal("accepted a counter above int32")
	}
	got, err := int32Counter(20)
	if err != nil || got != 20 {
		t.Fatalf("got %d err=%v", got, err)
	}
}

func stringsRepeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}

func utf8Valid(s string) bool {
	return len(clipUTF8(s, len(s))) == len(s)
}
