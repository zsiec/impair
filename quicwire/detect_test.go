package quicwire

import "testing"

func TestLooksEncrypted(t *testing.T) {
	cases := []struct {
		name string
		b0   byte // first byte of the datagram
		want bool
	}{
		// Long-header packets (form bit 0x80 set): Initial / Handshake / etc.
		// A real QUIC v1 Initial first byte is 0xC0..0xCF (long + fixed + type).
		{"initial-long-header", 0xC0, true},
		{"long-header-no-fixed", 0x80, true}, // Version Negotiation: long, fixed clear
		// Short-header (1-RTT) packets: form bit clear, fixed bit (0x40) set.
		// A real QUIC v1 short header first byte is 0x40..0x7F.
		{"short-header-1rtt", 0x40, true},
		{"short-header-with-spin", 0x60, true},
		// Bytes with neither the long-header nor the fixed bit set are not QUIC.
		{"not-quic", 0x00, false},
		{"rtp-like", 0x10, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte{tc.b0, 0x01, 0x02, 0x03}
			if got := LooksEncrypted(data); got != tc.want {
				t.Errorf("LooksEncrypted(first byte %#x) = %v, want %v", tc.b0, got, tc.want)
			}
		})
	}
}

func TestLooksEncryptedEmpty(t *testing.T) {
	if LooksEncrypted(nil) {
		t.Error("empty datagram should not LookEncrypted")
	}
	if LooksEncrypted([]byte{}) {
		t.Error("zero-length datagram should not LookEncrypted")
	}
}
