package cloud

// ValidSerial accepts 1-64 printable ASCII bytes, excluding space.
// Cloud identities and outgoing device requests share this local policy.
func ValidSerial(serial string) bool {
	if len(serial) == 0 || len(serial) > 64 {
		return false
	}
	for i := range len(serial) {
		if serial[i] < 0x21 || serial[i] > 0x7e {
			return false
		}
	}
	return true
}
