package wire

import (
	"encoding/binary"
	"reflect"
	"testing"
)

// be writes a sequence of 32-bit big-endian words into a byte slice.
func be(words ...uint32) []byte {
	b := make([]byte, 4*len(words))
	for i, w := range words {
		binary.BigEndian.PutUint32(b[4*i:], w)
	}
	return b
}

func TestDecodeTooShort(t *testing.T) {
	for _, n := range []int{0, 1, 8, 15} {
		if _, ok := Decode(make([]byte, n)); ok {
			t.Errorf("Decode(%d bytes) = ok, want not ok", n)
		}
	}
}

func TestDecodeData(t *testing.T) {
	// word0: F=0, seq=0x0034D2AB; word1: PP=11 O=1 KK=00 R=0 MsgNo=7.
	const seq = 0x0034D2AB
	pkt := be(seq, 0xE0000007, 0x00112233, 0x44556677)
	p, ok := Decode(pkt)
	if !ok {
		t.Fatal("Decode failed")
	}
	if p.IsControl {
		t.Error("IsControl = true, want false")
	}
	if p.Seq != seq {
		t.Errorf("Seq = %#x, want %#x", p.Seq, seq)
	}
	if p.Retrans {
		t.Error("Retrans = true, want false")
	}
}

func TestDecodeDataRetrans(t *testing.T) {
	// R bit is bit 26 of word1 (mask 0x04000000). This mirrors a real srtgo
	// retransmit observed as w1=0xE4000003 (vs 0xE0000003 for the original).
	const seq = 441826218 // 0x1A55BBAA, observed real retransmit seq
	pkt := be(seq, 0xE4000003, 0, 0)
	p, ok := Decode(pkt)
	if !ok {
		t.Fatal("Decode failed")
	}
	if p.IsControl || p.Seq != seq {
		t.Fatalf("got control=%v seq=%#x", p.IsControl, p.Seq)
	}
	if !p.Retrans {
		t.Error("Retrans = false, want true")
	}
	// Same seq without the R bit must not be flagged.
	p2, _ := Decode(be(seq, 0xE0000003, 0, 0))
	if p2.Retrans {
		t.Error("Retrans = true for non-retransmit packet")
	}
}

// ctrlWord0 builds a control word0 from a control type (subtype 0).
func ctrlWord0(ctrlType uint16) uint32 {
	return 0x80000000 | uint32(ctrlType)<<16
}

func TestDecodeACK(t *testing.T) {
	const ackSeq = 0x12345678
	// header (4 words) + CIF whose first word is the acknowledged seq.
	pkt := append(be(ctrlWord0(CtrlACK), 0x00000001, 0, 0), be(ackSeq)...)
	p, ok := Decode(pkt)
	if !ok {
		t.Fatal("Decode failed")
	}
	if !p.IsControl {
		t.Error("IsControl = false, want true")
	}
	if p.ControlType != CtrlACK {
		t.Errorf("ControlType = %#x, want %#x", p.ControlType, CtrlACK)
	}
	if p.AckSeq != ackSeq {
		t.Errorf("AckSeq = %#x, want %#x", p.AckSeq, ackSeq)
	}
}

func TestDecodeACKNoCIF(t *testing.T) {
	// A lightweight ACK with no CIF must decode without panicking.
	p, ok := Decode(be(ctrlWord0(CtrlACK), 0, 0, 0))
	if !ok || !p.IsControl || p.ControlType != CtrlACK {
		t.Fatalf("got ok=%v %+v", ok, p)
	}
	if p.AckSeq != 0 {
		t.Errorf("AckSeq = %#x, want 0", p.AckSeq)
	}
}

func TestDecodeNAKSingle(t *testing.T) {
	// Two single lost seqs (MSB clear). Mirrors real srtgo NAK CIF 0x1A55BBAA.
	pkt := append(be(ctrlWord0(CtrlNAK), 0, 0, 0), be(0x1A55BBAA, 100)...)
	p, ok := Decode(pkt)
	if !ok {
		t.Fatal("Decode failed")
	}
	if p.ControlType != CtrlNAK {
		t.Errorf("ControlType = %#x, want %#x", p.ControlType, CtrlNAK)
	}
	want := []uint32{0x1A55BBAA, 100}
	if !reflect.DeepEqual(p.NakSeqs, want) {
		t.Errorf("NakSeqs = %v, want %v", p.NakSeqs, want)
	}
}

func TestDecodeNAKRange(t *testing.T) {
	// Range start word has MSB set; low 31 bits = start; next word = end.
	// CIF: [0x80000000|10][12] then single 20.
	pkt := append(be(ctrlWord0(CtrlNAK), 0, 0, 0), be(0x80000000|10, 12, 20)...)
	p, ok := Decode(pkt)
	if !ok {
		t.Fatal("Decode failed")
	}
	want := []uint32{10, 11, 12, 20}
	if !reflect.DeepEqual(p.NakSeqs, want) {
		t.Errorf("NakSeqs = %v, want %v", p.NakSeqs, want)
	}
}

func TestDecodeNAKRangeSingleElement(t *testing.T) {
	// A range where start == end expands to one element.
	pkt := append(be(ctrlWord0(CtrlNAK), 0, 0, 0), be(0x80000000|5, 5)...)
	p, _ := Decode(pkt)
	if want := []uint32{5}; !reflect.DeepEqual(p.NakSeqs, want) {
		t.Errorf("NakSeqs = %v, want %v", p.NakSeqs, want)
	}
}

func TestDecodeHandshake(t *testing.T) {
	p, ok := Decode(be(ctrlWord0(CtrlHandshake), 0, 0, 0))
	if !ok || !p.IsControl || p.ControlType != CtrlHandshake {
		t.Fatalf("got ok=%v %+v", ok, p)
	}
}

func TestDecodeKeepalive(t *testing.T) {
	p, ok := Decode(be(ctrlWord0(CtrlKeepalive), 0, 0, 0))
	if !ok || !p.IsControl || p.ControlType != CtrlKeepalive {
		t.Fatalf("got ok=%v %+v", ok, p)
	}
}

func TestDecodeShutdown(t *testing.T) {
	p, ok := Decode(be(ctrlWord0(CtrlShutdown), 0, 0, 0))
	if !ok || !p.IsControl || p.ControlType != CtrlShutdown {
		t.Fatalf("got ok=%v %+v", ok, p)
	}
}

func TestDecodeACKACK(t *testing.T) {
	p, ok := Decode(be(ctrlWord0(CtrlACKACK), 0, 0, 0))
	if !ok || !p.IsControl || p.ControlType != CtrlACKACK {
		t.Fatalf("got ok=%v %+v", ok, p)
	}
}

func TestDecodeControlTypeMasking(t *testing.T) {
	// The control type is 15 bits; the F bit must not bleed into it, and a
	// non-zero subtype (low 16 bits) must be ignored for the type.
	word0 := 0x80000000 | uint32(CtrlUser)<<16 | 0xABCD
	p, ok := Decode(be(word0, 0, 0, 0))
	if !ok || p.ControlType != CtrlUser {
		t.Fatalf("ControlType = %#x, want %#x", p.ControlType, CtrlUser)
	}
}
