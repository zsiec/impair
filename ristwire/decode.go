package ristwire

import "encoding/binary"

// RIST Simple Profile is RTP-over-UDP with RTCP-based retransmission. The relay
// sees cleartext RTP and RTCP datagrams; this file decodes one datagram's
// header into a Pkt.
//
// RTP and RTCP are demultiplexed by the second byte of the datagram (RFC 5761):
// for RTCP that byte is the packet type (PT) in the range 192..223 (SR=200 …
// PSFB=206); for RTP the low 7 bits of that byte are the payload type, which by
// the RFC 5761 reservation never collides with the RTCP range. We classify any
// datagram whose byte-1 value is in [192,223] as RTCP, everything else as RTP.
//
// RTP header (RFC 3550, 12 bytes minimum):
//
//	byte0:   [V:2][P:1][X:1][CC:4]
//	byte1:   [M:1][PT:7]
//	byte2-3: sequence number (16-bit, big-endian)
//	byte4-7: timestamp (32-bit)
//	byte8-11: SSRC (32-bit)
//
// RTCP header (RFC 3550, 8 bytes for the common header + sender SSRC):
//
//	byte0:   [V:2][P:1][RC/FMT:5]
//	byte1:   PT (200..206)
//	byte2-3: length (in 32-bit words minus one)
//	byte4-7: SSRC of packet sender
//
// For a transport-layer feedback packet (RTPFB, PT=205) the format (FMT, the
// low 5 bits of byte0) selects the feedback type; FMT=1 is the Generic NACK
// (RFC 4585). Its body is the 4-byte media-source SSRC (byte8-11) followed by
// one or more 32-bit FCI entries: a 16-bit PID (a lost RTP sequence number)
// and a 16-bit BLP bitmask whose bit i (i=0..15) flags the loss of sequence
// number PID+1+i. RIST uses this as its retransmit request.

const (
	rtpHeaderLen  = 12
	rtcpHeaderLen = 8

	// rtcpPTLow/High bound the RTCP packet-type range used for RTP/RTCP
	// demultiplexing on a shared port (RFC 5761).
	rtcpPTLow  = 192
	rtcpPTHigh = 223

	fmtGenericNACK = 1 // RTPFB FMT for the RFC 4585 Generic NACK
)

// DTLS record-layer constants used by the best-effort encrypted-flow detector.
// A DTLS record begins with a 1-byte content type, a 2-byte protocol version,
// then a 2-byte epoch, 6-byte sequence number, 2-byte length, and the
// (encrypted, for handshake-after-CCS and application data) fragment.
const (
	dtlsHandshake = 0x16 // ContentType handshake
	dtlsAppData   = 0x17 // ContentType application_data (the encrypted media)
	dtlsChangeCS  = 0x14 // ContentType change_cipher_spec
	dtlsAlert     = 0x15 // ContentType alert

	// DTLS major version is always 0xFE (1's-complement encoding of TLS major).
	// Minor: 0xFF = DTLS 1.0, 0xFD = DTLS 1.2 (and the 1.3 record's legacy field).
	dtlsVersionMajor = 0xFE
)

// LooksEncrypted is a best-effort, header-only detector the runner can use to
// auto-label a RIST flow's media plane as opaque. RIST's "Main Profile" wraps
// the RTP/RTCP in DTLS; a DTLS record starts with a content-type byte
// (0x14/0x15/0x16/0x17) followed by a DTLS protocol version whose major byte is
// 0xFE. We require BOTH the content-type and the version-major to match so a
// cleartext RTP/RTCP datagram (whose byte 0 is 0x80-ish and byte 1 is a payload
// type) is never misclassified. It is conservative by design: anything it
// cannot positively identify as DTLS returns false.
func LooksEncrypted(data []byte) bool {
	if len(data) < 3 {
		return false
	}
	switch data[0] {
	case dtlsHandshake, dtlsAppData, dtlsChangeCS, dtlsAlert:
	default:
		return false
	}
	// data[1] is the DTLS version major (0xFE); data[2] is the minor.
	return data[1] == dtlsVersionMajor
}

// Decode parses one RTP or RTCP datagram header. It returns the decoded Pkt and
// true, or a zero Pkt and false if the datagram is too short to hold a header
// (an RTCP packet needs >= 8 bytes; an RTP packet needs >= 12).
func Decode(data []byte) (Pkt, bool) {
	// Need at least the first two bytes to read the demux (PT) byte.
	if len(data) < 2 {
		return Pkt{}, false
	}

	pt := data[1]
	if pt >= rtcpPTLow && pt <= rtcpPTHigh {
		return decodeRTCP(data)
	}

	if len(data) < rtpHeaderLen {
		return Pkt{}, false
	}

	// RTP: PT is the low 7 bits of byte1.
	p := Pkt{
		IsRTCP:      false,
		PayloadType: data[1] & 0x7f,
		Seq:         binary.BigEndian.Uint16(data[2:4]),
		Timestamp:   binary.BigEndian.Uint32(data[4:8]),
		SSRC:        binary.BigEndian.Uint32(data[8:12]),
	}
	return p, true
}

// decodeRTCP parses an RTCP datagram. The minimum RTCP packet is 8 bytes (the
// common header plus sender SSRC); a datagram already known to be >= 12 bytes
// (Decode's guard) trivially satisfies that.
func decodeRTCP(data []byte) (Pkt, bool) {
	if len(data) < rtcpHeaderLen {
		return Pkt{}, false
	}

	pt := data[1]
	p := Pkt{
		IsRTCP:      true,
		PayloadType: pt,
		SSRC:        binary.BigEndian.Uint32(data[4:8]),
	}

	// Transport-layer feedback (RTPFB): parse a Generic NACK (FMT=1) into the
	// expanded list of requested sequence numbers.
	if pt == RTCPRTPFB {
		fmt := data[0] & 0x1f
		if fmt == fmtGenericNACK {
			p.NackSeqs = parseGenericNACK(data)
		}
	}
	return p, true
}

// parseGenericNACK expands the FCI entries of an RFC 4585 Generic NACK into the
// flat list of requested RTP sequence numbers. The FCI list begins at byte 12
// (after the 8-byte common header and the 4-byte media-source SSRC); each entry
// is a 16-bit PID + a 16-bit BLP. Wrap-around is handled by uint16 arithmetic.
func parseGenericNACK(data []byte) []uint16 {
	const fciStart = 12
	if len(data) < fciStart+4 {
		return nil
	}

	var seqs []uint16
	for off := fciStart; off+4 <= len(data); off += 4 {
		pid := binary.BigEndian.Uint16(data[off : off+2])
		blp := binary.BigEndian.Uint16(data[off+2 : off+4])
		seqs = append(seqs, pid)
		for i := 0; i < 16; i++ {
			if blp&(1<<uint(i)) != 0 {
				seqs = append(seqs, pid+uint16(i)+1)
			}
		}
	}
	return seqs
}
