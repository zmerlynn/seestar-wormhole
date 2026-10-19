package p2ptunnel

const auditUnknownStream = "0a000100ff02070300616263"
const auditFamilySetup = "010710009900125c7f0000010000000000000000"
const auditTailSetup = "010735000200125c7f0000010000000000000000000000000000000000000000000000000000000000000000000000000000000000000000aa"
const auditClose = "0407020002aa"
const auditLegacyPass = "0800040000000000"
const auditChannelPass = "080708000000000001000000"
const auditChannelPort = "0d07060034125c120156"
const auditChannelService = "0f072400010078000000000000000000000000000000000000000000000000000000000000000001"

func compatibilitySeeds() []struct {
	wire  string
	phase Phase
} {
	return []struct {
		wire  string
		phase Phase
	}{
		{"0a000100ff", Established}, {auditUnknownStream, Established},
		{auditFamilySetup, Established}, {auditTailSetup, Established}, {auditClose, Established},
		{auditLegacyPass, LoginResponse}, {auditChannelPass, LoginResponse}, {auditChannelPort, Established}, {auditChannelService, Established},
		{"070708000008030400000000", LoginRequest},
	}
}
