package hikvision

import (
	"math/rand"
	"testing"
)

func TestPickStandaloneSubEvent_OnlyKnownMinors(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	allowed := map[int]bool{
		MinorLegalCardPass:   true,
		MinorCardOutOfDate:   true,
		MinorInvalidCard:     true,
		MinorFingerprintPass: true,
		MinorFingerprintFail: true,
		MinorFaceVerifyPass:  true,
		MinorFaceVerifyFail:  true,
	}
	for i := 0; i < 2000; i++ {
		got := pickStandaloneSubEvent(r)
		if !allowed[got] {
			t.Fatalf("draw returned unknown subEventType: %d", got)
		}
	}
}

func TestPickStandaloneSubEvent_GrantDominates(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	grants := 0
	const n = 2000
	for i := 0; i < n; i++ {
		if subEventIsGrant(pickStandaloneSubEvent(r)) {
			grants++
		}
	}
	// The vast majority of accesses on a real device are granted.
	if grants < n*6/10 {
		t.Errorf("granted accesses: %d of %d, expected at least 60 percent", grants, n)
	}
	if grants == n {
		t.Error("no denial drawn in 2000 samples; variety is not active")
	}
}

func TestSubEventIsGrant(t *testing.T) {
	cases := map[int]bool{
		MinorLegalCardPass:   true,
		MinorFingerprintPass: true,
		MinorFaceVerifyPass:  true,
		MinorInvalidCard:     false,
		MinorFaceVerifyFail:  false,
		MinorFingerprintFail: false,
		MinorCardOutOfDate:   false,
	}
	for minor, want := range cases {
		if got := subEventIsGrant(minor); got != want {
			t.Errorf("subEventIsGrant(%d): got %v, want %v", minor, got, want)
		}
	}
}
