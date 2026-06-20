package ristwire

import "testing"

// dtlsRecord builds a minimal DTLS record header: content type, version, then
// padding so the slice is long enough to be a plausible record.
func dtlsRecord(contentType, verMajor, verMinor byte) []byte {
	return []byte{contentType, verMajor, verMinor, 0x00, 0x00, 0x00, 0x00}
}

func TestLooksEncryptedDTLS(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		// DTLS 1.2 (version major 0xFE, minor 0xFD) records of each content type.
		{"app-data-1.2", dtlsRecord(dtlsAppData, 0xFE, 0xFD), true},
		{"handshake-1.2", dtlsRecord(dtlsHandshake, 0xFE, 0xFD), true},
		{"change-cipher-1.2", dtlsRecord(dtlsChangeCS, 0xFE, 0xFD), true},
		{"alert-1.2", dtlsRecord(dtlsAlert, 0xFE, 0xFD), true},
		// DTLS 1.0 record (version minor 0xFF) is still encrypted transport.
		{"app-data-1.0", dtlsRecord(dtlsAppData, 0xFE, 0xFF), true},
		// A content-type byte that is not a DTLS record type -> not detected.
		{"non-dtls-content-type", dtlsRecord(0x42, 0xFE, 0xFD), false},
		// DTLS content type but wrong version major -> reject (avoids matching a
		// random datagram that merely happens to start with 0x16/0x17).
		{"wrong-version-major", dtlsRecord(dtlsAppData, 0xAB, 0xCD), false},
		// Cleartext RTP (byte0 0x80, byte1 a payload type) must NOT look encrypted.
		{"rtp-not-encrypted", rtpPacket(96, 1, 0, 0xDEADBEEF), false},
		// Cleartext RTCP SR likewise.
		{"rtcp-not-encrypted", rtcpSR(0xDEADBEEF), false},
		// Too short to hold a content type + version.
		{"too-short", []byte{dtlsAppData, 0xFE}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LooksEncrypted(tc.data); got != tc.want {
				t.Errorf("LooksEncrypted(%v) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}
