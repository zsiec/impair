// Package ristwire is the passive RIST (Reliable Internet Stream Transport)
// wire observer — the RIST analogue of the SRT `wire` package. RIST Simple
// Profile is RTP-over-UDP with RTCP-based retransmission (the receiver requests
// lost sequence numbers via an RTCP transport-layer feedback "NACK"; the sender
// retransmits the RTP packets). This decoder reads the cleartext RTP/RTCP
// headers at the relay and accumulates the protocol facts the RIST oracle judges
// — cloning the SRT template (relay -> observer -> oracle -> matrix) to a second
// protocol.
package ristwire

import "github.com/zsiec/impair/wireobs"

// RTCP packet types (the 8-bit PT field of an RTCP packet).
const (
	RTCPSenderReport   uint8 = 200
	RTCPReceiverReport uint8 = 201
	RTCPSDES           uint8 = 202
	RTCPBye            uint8 = 203
	RTCPApp            uint8 = 204
	RTCPRTPFB          uint8 = 205 // transport-layer feedback (RFC 4585) — RIST range NACK
	RTCPPSFB           uint8 = 206
)

// Pkt is a decoded RTP or RTCP packet header (cleartext fields only).
type Pkt struct {
	IsRTCP      bool
	Seq         uint16   // RTP sequence number (RTP only)
	Timestamp   uint32   // RTP timestamp (RTP only)
	SSRC        uint32   // RTP SSRC / RTCP sender SSRC
	PayloadType uint8    // RTP payload type, or the RTCP packet type for RTCP
	NackSeqs    []uint16 // RTPFB generic-NACK requested sequence numbers, expanded (RTCP only)
}

// Observation accumulates the RIST wire facts over one run. The RTP/RTCP packet
// split, NACK count, retransmissions, and the requested/retransmitted sequence
// sets live in the embedded wireobs.Counters (RIST RTP packets are
// Counters.DataPackets, RTCP packets are Counters.ControlPackets, NACKs are
// Counters.RetransReqs, NACK'd seqs are Counters.ReqSeqs); the fields below are
// RIST-specific.
type Observation struct {
	wireobs.Counters[uint16]
	SenderReports   int
	ReceiverReports int
	SeqGaps         int // missing sequence numbers in the observed RTP stream
	SSRCs           map[uint32]bool
	MaxSeq          uint16
	SeqSeen         bool
}
