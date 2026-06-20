// Package wire is the passive SRT wire observer: it decodes the cleartext SRT
// control/data headers flowing through the relay (it never needs the payload,
// which may be encrypted) and accumulates the protocol facts the oracle judges.
// This is the "protocol-aware" half of the moat — it turns "packets moved" into
// "the ACK/NAK/retransmit machinery behaved correctly".
package wire

// SRT control packet types (the 15-bit control type field).
const (
	CtrlHandshake  uint16 = 0x0000
	CtrlKeepalive  uint16 = 0x0001
	CtrlACK        uint16 = 0x0002
	CtrlNAK        uint16 = 0x0003 // loss report
	CtrlCongestion uint16 = 0x0004 // congestion warning
	CtrlShutdown   uint16 = 0x0005
	CtrlACKACK     uint16 = 0x0006
	CtrlDropReq    uint16 = 0x0007
	CtrlPeerError  uint16 = 0x0008
	CtrlUser       uint16 = 0x7FFF
)

// Pkt is a decoded SRT packet header (cleartext fields only).
type Pkt struct {
	IsControl   bool
	Seq         uint32   // data packet sequence number (data only)
	Retrans     bool     // data retransmission flag (data only)
	KK          uint8    // key-based encryption flag (data only): 0=clear, 1=even key, 2=odd key
	ControlType uint16   // control packet type (control only)
	AckSeq      uint32   // ACK: the acknowledged sequence number (control only)
	NakSeqs     []uint32 // NAK: reported-lost sequence numbers, ranges expanded
}

// Encrypted reports whether this packet's payload is KM-encrypted. Only DATA
// packets carry a payload; the KK field is nonzero (even or odd key) when SRT's
// AES-CTR encryption is in use. Control packets are never payload-encrypted.
func (p Pkt) Encrypted() bool { return !p.IsControl && p.KK != 0 }

// LooksEncrypted is a best-effort, header-only detector the runner can use to
// auto-label an SRT flow's media plane as opaque. It returns true if the
// datagram is a well-formed DATA packet whose KK (key-based encryption) field is
// nonzero. It is intentionally conservative: a datagram it cannot decode, or a
// control packet, yields false (control packets are unencrypted and a single
// undecodable datagram should not flip the whole flow's label). Callers
// typically OR this over the first handful of DATA packets of a flow.
func LooksEncrypted(data []byte) bool {
	p, ok := Decode(data)
	return ok && p.Encrypted()
}

// Observation accumulates the wire facts over one run, from one Observer.
type Observation struct {
	DataPackets    int
	ControlPackets int
	Handshakes     int
	ACKs           int
	NAKs           int
	KeepAlives     int
	Shutdowns      int
	AckAcks        int
	Retransmitted  int             // data packets seen with the retransmit flag set
	NakedSeqs      map[uint32]bool // sequence numbers that appeared in a NAK
	RetransSeqs    map[uint32]bool // sequence numbers seen retransmitted
	MaxAckSeq      uint32          // highest ACK'd sequence number observed
	AckMonotonic   bool            // ACK'd sequence numbers never went backwards
}
