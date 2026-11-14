package hikvision

import "math/rand"

// Access-control event subtypes (majorEventType=5). Only the ones an
// access-control client actually consumes are listed here.
const (
	MinorLegalCardPass    = 1  // valid card
	MinorCardOutOfDate    = 8  // expired card
	MinorInvalidCard      = 9  // unknown card
	MinorDoorOpenNormal   = 25 // door opened (sensor)
	MinorDoorCloseNormal  = 26 // door closed (sensor)
	MinorDoorOpenAbnormal = 27 // door forced open
	MinorFingerprintPass  = 38 // fingerprint matched
	MinorFingerprintFail  = 39 // fingerprint rejected
	MinorFaceVerifyPass   = 75 // face matched
	MinorFaceVerifyFail   = 76 // face rejected
)

// subEventWeights is the distribution used in standalone mode. A production
// device grants the vast majority of accesses; the denials exist to exercise
// the client's INVALID_CARD / BIOMETRIC_VERIFICATION_FAILED / CARD_EXPIRED
// paths.
var subEventWeights = []struct {
	minor  int
	weight int
}{
	{MinorFaceVerifyPass, 55},
	{MinorLegalCardPass, 15},
	{MinorFingerprintPass, 10},
	{MinorInvalidCard, 8},
	{MinorFaceVerifyFail, 6},
	{MinorFingerprintFail, 3},
	{MinorCardOutOfDate, 3},
}

// pickStandaloneSubEvent draws an access subEventType according to the weights.
func pickStandaloneSubEvent(r *rand.Rand) int {
	total := 0
	for _, p := range subEventWeights {
		total += p.weight
	}
	n := r.Intn(total)
	for _, p := range subEventWeights {
		if n < p.weight {
			return p.minor
		}
		n -= p.weight
	}
	return MinorFaceVerifyPass
}

// subEventIsGrant reports whether the subtype represents a granted access.
// Clients only treat these three as authorized.
func subEventIsGrant(minor int) bool {
	switch minor {
	case MinorLegalCardPass, MinorFingerprintPass, MinorFaceVerifyPass:
		return true
	}
	return false
}
