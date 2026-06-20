package wire

import "encoding/binary"

// SRT packets are framed as 32-bit big-endian words. Every packet carries a
// 16-byte header (four words):
//
//	word0: [F:1][...] — F (MSB) is the packet type: 0 = DATA, 1 = CONTROL.
//	word1: type-specific.
//	word2: timestamp (microseconds since socket start).
//	word3: destination socket id.
//
// DATA packet (F=0):
//
//	word0: [0:1][packet sequence number:31]
//	word1: [PP:2][O:1][KK:2][R:1][message number:26]
//	       PP = packet position flag, O = order flag, KK = key-based encryption
//	       flag, R = retransmit flag (set when this packet is a retransmission).
//	word2: timestamp
//	word3: destination socket id
//	payload follows.
//
// CONTROL packet (F=1):
//
//	word0: [1:1][control type:15][subtype:16]
//	word1: type-specific information (e.g. ACK sub-sequence number)
//	word2: timestamp
//	word3: destination socket id
//	CIF (control information field) follows.
//
// For an ACK (type 0x0002) the first CIF word is the acknowledged sequence
// number: the sequence number of the last packet received + 1.
//
// For a NAK / loss report (type 0x0003) the CIF is a list of 32-bit words.
// A word with its MSB clear is a single lost sequence number. A word with its
// MSB set begins a range: its low 31 bits are the (inclusive) range start and
// the following word is the (inclusive) range end.

const headerLen = 16

// retransMask isolates the R (retransmit) bit in a DATA packet's word1.
const retransMask = 1 << 26

// kkMask isolates the KK (key-based encryption) field in a DATA packet's word1.
// word1 is [PP:2][O:1][KK:2][R:1][message number:26], so the two KK bits sit at
// positions 28..27. KK == 0 means the payload is cleartext; KK == 1 (even key)
// or KK == 2 (odd key) means it is KM-encrypted. (KK == 3 is reserved.)
const kkMask = 0b11 << 27

// Decode parses one SRT datagram's header. It returns the decoded Pkt and true,
// or a zero Pkt and false if the datagram is too short to contain a header.
func Decode(data []byte) (Pkt, bool) {
	if len(data) < headerLen {
		return Pkt{}, false
	}

	word0 := binary.BigEndian.Uint32(data[0:4])
	word1 := binary.BigEndian.Uint32(data[4:8])

	if word0&0x80000000 == 0 {
		// DATA packet.
		p := Pkt{
			IsControl: false,
			Seq:       word0 & 0x7FFFFFFF,
			Retrans:   word1&retransMask != 0,
			KK:        uint8((word1 & kkMask) >> 27),
		}
		return p, true
	}

	// CONTROL packet. Control type is the low 15 bits of word0's high half.
	p := Pkt{
		IsControl:   true,
		ControlType: uint16((word0 >> 16) & 0x7FFF),
	}

	switch p.ControlType {
	case CtrlACK:
		// First CIF word (if present) is the acknowledged sequence number.
		if len(data) >= headerLen+4 {
			p.AckSeq = binary.BigEndian.Uint32(data[headerLen : headerLen+4])
		}
	case CtrlNAK:
		p.NakSeqs = decodeNak(data[headerLen:])
	}

	return p, true
}

// decodeNak expands an SRT loss-report CIF into the flat list of lost sequence
// numbers. Single losses are listed directly; ranges (high-bit-set start word
// followed by an end word) are expanded inclusively.
func decodeNak(cif []byte) []uint32 {
	var seqs []uint32
	for i := 0; i+4 <= len(cif); i += 4 {
		w := binary.BigEndian.Uint32(cif[i : i+4])
		if w&0x80000000 == 0 {
			// Single lost sequence number.
			seqs = append(seqs, w)
			continue
		}
		// Range: low 31 bits of this word = start, next word = end (inclusive).
		start := w & 0x7FFFFFFF
		if i+8 > len(cif) {
			// Malformed: range start with no end word. Treat start as single.
			seqs = append(seqs, start)
			break
		}
		i += 4
		end := binary.BigEndian.Uint32(cif[i:i+4]) & 0x7FFFFFFF
		if end < start {
			seqs = append(seqs, start)
			continue
		}
		for s := start; ; s++ {
			seqs = append(seqs, s)
			if s == end {
				break
			}
		}
	}
	return seqs
}
