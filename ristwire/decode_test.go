package ristwire

import (
	"encoding/binary"
	"reflect"
	"testing"
)

// rtpPacket hand-builds a minimal 12-byte RTP header datagram.
func rtpPacket(pt uint8, seq uint16, ts, ssrc uint32) []byte {
	b := make([]byte, rtpHeaderLen)
	b[0] = 0x80      // V=2, P=0, X=0, CC=0
	b[1] = pt & 0x7f // M=0, PT
	binary.BigEndian.PutUint16(b[2:4], seq)
	binary.BigEndian.PutUint32(b[4:8], ts)
	binary.BigEndian.PutUint32(b[8:12], ssrc)
	return b
}

// rtcpSR hand-builds an RTCP Sender Report common header + sender SSRC.
func rtcpSR(ssrc uint32) []byte {
	b := make([]byte, rtcpHeaderLen)
	b[0] = 0x80 // V=2, P=0, RC=0
	b[1] = RTCPSenderReport
	binary.BigEndian.PutUint16(b[2:4], 1) // length in words-1
	binary.BigEndian.PutUint32(b[4:8], ssrc)
	return b
}

// rtcpRR hand-builds an RTCP Receiver Report common header + sender SSRC.
func rtcpRR(ssrc uint32) []byte {
	b := make([]byte, rtcpHeaderLen)
	b[0] = 0x80
	b[1] = RTCPReceiverReport
	binary.BigEndian.PutUint16(b[2:4], 1)
	binary.BigEndian.PutUint32(b[4:8], ssrc)
	return b
}

// rtcpNACK hand-builds an RFC 4585 RTPFB Generic NACK (FMT=1) carrying the
// given FCI (PID, BLP) entries.
func rtcpNACK(senderSSRC, mediaSSRC uint32, fci ...[2]uint16) []byte {
	b := make([]byte, 12+4*len(fci))
	b[0] = 0x80 | fmtGenericNACK // V=2, P=0, FMT=1
	b[1] = RTCPRTPFB
	binary.BigEndian.PutUint16(b[2:4], uint16(2+len(fci)))
	binary.BigEndian.PutUint32(b[4:8], senderSSRC)
	binary.BigEndian.PutUint32(b[8:12], mediaSSRC)
	for i, e := range fci {
		off := 12 + 4*i
		binary.BigEndian.PutUint16(b[off:off+2], e[0])
		binary.BigEndian.PutUint16(b[off+2:off+4], e[1])
	}
	return b
}

func TestDecodeTooShort(t *testing.T) {
	for _, n := range []int{0, 1, 7, 11} {
		if _, ok := Decode(make([]byte, n)); ok {
			t.Errorf("Decode(%d bytes) = ok, want not ok", n)
		}
	}
}

func TestDecodeRTP(t *testing.T) {
	const (
		pt   = uint8(96)
		seq  = uint16(0x1234)
		ts   = uint32(0xDEADBEEF)
		ssrc = uint32(0x11223344)
	)
	p, ok := Decode(rtpPacket(pt, seq, ts, ssrc))
	if !ok {
		t.Fatal("Decode failed")
	}
	if p.IsRTCP {
		t.Error("IsRTCP = true, want false")
	}
	if p.PayloadType != pt {
		t.Errorf("PayloadType = %d, want %d", p.PayloadType, pt)
	}
	if p.Seq != seq {
		t.Errorf("Seq = %#x, want %#x", p.Seq, seq)
	}
	if p.Timestamp != ts {
		t.Errorf("Timestamp = %#x, want %#x", p.Timestamp, ts)
	}
	if p.SSRC != ssrc {
		t.Errorf("SSRC = %#x, want %#x", p.SSRC, ssrc)
	}
	if len(p.NackSeqs) != 0 {
		t.Errorf("NackSeqs = %v, want empty", p.NackSeqs)
	}
}

// TestDecodeRTPMarkerBit verifies the marker bit (byte1 MSB) does not leak into
// the payload type.
func TestDecodeRTPMarkerBit(t *testing.T) {
	b := rtpPacket(96, 1, 0, 0)
	b[1] |= 0x80 // set marker
	p, ok := Decode(b)
	if !ok {
		t.Fatal("Decode failed")
	}
	if p.IsRTCP {
		t.Error("marker bit misread as RTCP")
	}
	if p.PayloadType != 96 {
		t.Errorf("PayloadType = %d, want 96", p.PayloadType)
	}
}

func TestDecodeRTCPSR(t *testing.T) {
	p, ok := Decode(rtcpSR(0xAABBCCDD))
	if !ok {
		t.Fatal("Decode failed")
	}
	if !p.IsRTCP {
		t.Error("IsRTCP = false, want true")
	}
	if p.PayloadType != RTCPSenderReport {
		t.Errorf("PayloadType = %d, want %d", p.PayloadType, RTCPSenderReport)
	}
	if p.SSRC != 0xAABBCCDD {
		t.Errorf("SSRC = %#x, want 0xAABBCCDD", p.SSRC)
	}
}

func TestDecodeRTCPRR(t *testing.T) {
	p, ok := Decode(rtcpRR(0x01020304))
	if !ok {
		t.Fatal("Decode failed")
	}
	if !p.IsRTCP || p.PayloadType != RTCPReceiverReport {
		t.Fatalf("got IsRTCP=%v PT=%d", p.IsRTCP, p.PayloadType)
	}
	if p.SSRC != 0x01020304 {
		t.Errorf("SSRC = %#x, want 0x01020304", p.SSRC)
	}
}

// TestDecodeNACKSingle: a Generic NACK with BLP=0 requests exactly the PID.
func TestDecodeNACKSingle(t *testing.T) {
	p, ok := Decode(rtcpNACK(1, 2, [2]uint16{1000, 0}))
	if !ok {
		t.Fatal("Decode failed")
	}
	if !p.IsRTCP || p.PayloadType != RTCPRTPFB {
		t.Fatalf("got IsRTCP=%v PT=%d", p.IsRTCP, p.PayloadType)
	}
	if want := []uint16{1000}; !reflect.DeepEqual(p.NackSeqs, want) {
		t.Errorf("NackSeqs = %v, want %v", p.NackSeqs, want)
	}
}

// TestDecodeNACKBitmask: BLP bit i flags loss of PID+1+i.
func TestDecodeNACKBitmask(t *testing.T) {
	// bits 0 and 2 set -> PID+1 and PID+3, plus the PID itself.
	blp := uint16(0b0000000000000101)
	p, ok := Decode(rtcpNACK(1, 2, [2]uint16{100, blp}))
	if !ok {
		t.Fatal("Decode failed")
	}
	want := []uint16{100, 101, 103}
	if !reflect.DeepEqual(p.NackSeqs, want) {
		t.Errorf("NackSeqs = %v, want %v", p.NackSeqs, want)
	}
}

// TestDecodeNACKBitmaskTop: bit 15 flags loss of PID+16.
func TestDecodeNACKBitmaskTop(t *testing.T) {
	blp := uint16(1 << 15)
	p, _ := Decode(rtcpNACK(1, 2, [2]uint16{50, blp}))
	want := []uint16{50, 66}
	if !reflect.DeepEqual(p.NackSeqs, want) {
		t.Errorf("NackSeqs = %v, want %v", p.NackSeqs, want)
	}
}

// TestDecodeNACKWrap: PID near the top of the sequence space wraps via uint16.
func TestDecodeNACKWrap(t *testing.T) {
	// PID=0xFFFF, bit0 -> 0x0000, bit1 -> 0x0001.
	blp := uint16(0b11)
	p, _ := Decode(rtcpNACK(1, 2, [2]uint16{0xFFFF, blp}))
	want := []uint16{0xFFFF, 0x0000, 0x0001}
	if !reflect.DeepEqual(p.NackSeqs, want) {
		t.Errorf("NackSeqs = %v, want %v", p.NackSeqs, want)
	}
}

// TestDecodeNACKMultiFCI: multiple FCI entries are concatenated.
func TestDecodeNACKMultiFCI(t *testing.T) {
	p, _ := Decode(rtcpNACK(1, 2, [2]uint16{10, 0}, [2]uint16{20, 0b1}))
	want := []uint16{10, 20, 21}
	if !reflect.DeepEqual(p.NackSeqs, want) {
		t.Errorf("NackSeqs = %v, want %v", p.NackSeqs, want)
	}
}
