package oggpage

import "testing"

func TestCRC(t *testing.T) {
	// Independently computed vectors for the Ogg CRC polynomial
	// (0x04c11db7, non-reflected, init/xorout zero).
	cases := map[string]uint32{
		"":          0x00000000,
		"123456789": 0x89a1897f,
	}
	for in, want := range cases {
		if got := CRC([]byte(in)); got != want {
			t.Errorf("CRC(%q) = 0x%08x, want 0x%08x", in, got, want)
		}
	}
}
