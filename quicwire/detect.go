// Package quicwire is the QUIC-side counterpart to the SRT `wire` and RIST
// `ristwire` observers. QUIC (and therefore MoQ-over-QUIC) encrypts its entire
// payload — every packet past the initial handshake is protected, and even
// Initial packets are AEAD-encrypted under a published, version-derived key.
// There is consequently no cleartext media plane to observe; this package
// provides only a best-effort header sniff so the runner can auto-label a QUIC
// flow as encrypted (its media plane is always opaque to the relay).
package quicwire

// QUIC packets carry their form in the most-significant bit of the first byte
// (RFC 9000 §17): 1 = long header (handshake-era packets: Initial, 0-RTT,
// Handshake, Retry), 0 = short header (1-RTT, the steady-state data packets).
// The next bit (0x40) is the "fixed bit", set to 1 on every QUIC v1 packet
// EXCEPT a Version Negotiation packet (whose first byte is otherwise
// unconstrained). Either way the payload is encrypted.
const (
	longHeaderBit = 0x80 // form bit: 1 = long header
	fixedBit      = 0x40 // QUIC v1 fixed bit (1 on all non-VN packets)
)

// LooksEncrypted is a best-effort detector the runner can use to auto-label a
// QUIC / MoQ flow's media plane as opaque. Because EVERY QUIC packet is
// encrypted, the useful question is merely "does this look like QUIC?", and for
// labelling purposes the safe answer is "assume encrypted". We accept any
// datagram whose first byte has the QUIC fixed bit set (the common case for v1
// long- and short-header packets) as well as any long-header packet (form bit
// set), which also covers Version Negotiation. An empty datagram is not QUIC.
//
// It is intentionally permissive: mislabelling a non-QUIC flow as encrypted only
// blocks payload-selective cells (of which there are none today), whereas
// mislabelling an encrypted flow as cleartext would let a future selective cell
// silently no-op on opaque bytes — the failure mode the guard exists to prevent.
func LooksEncrypted(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	b := data[0]
	if b&longHeaderBit != 0 {
		// Long header: Initial / 0-RTT / Handshake / Retry / Version Negotiation.
		return true
	}
	// Short header (1-RTT): the fixed bit distinguishes a QUIC v1 data packet
	// from arbitrary bytes whose top bit happens to be clear.
	return b&fixedBit != 0
}
