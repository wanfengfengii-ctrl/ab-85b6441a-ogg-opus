// Package testogg builds deterministic Ogg Opus byte streams for tests.
// It is production-shaped (real CRCs, real segment tables) but exposes
// knobs to deliberately corrupt individual fields.
package testogg

import (
	"encoding/binary"

	"github.com/acoustics/opus-audit/internal/oggpage"
)

// PageSpec describes one physical page to emit.
type PageSpec struct {
	Continued bool
	BOS       bool
	EOS       bool
	Granule   int64
	Sequence  int // 0 means "use the running page index"
	Serial    uint32
	// Packets are complete Ogg packets placed on this page. When Segs is
	// nil the builder computes a minimal segment table over them.
	Packets [][]byte
	// Segs/Body, when Segs is non-nil, override the segment table and body
	// verbatim (used to craft packets spanning page boundaries).
	Segs []byte
	Body []byte
	// CorruptCRC flips the first body byte after the CRC was computed so
	// the stored CRC no longer matches.
	CorruptCRC bool
	// BadCapture replaces the capture pattern with garbage.
	BadCapture bool
}

// Builder assembles pages sharing one serial number.
type Builder struct {
	Serial uint32
	pages  []PageSpec
}

// New returns a Builder with the usual serial.
func New() *Builder { return &Builder{Serial: 0x5c7373d0} }

// Page appends a page specification.
func (b *Builder) Page(spec PageSpec) *Builder {
	b.pages = append(b.pages, spec)
	return b
}

// Spec returns a pointer to the specification of page i for mutation.
func (b *Builder) Spec(i int) *PageSpec { return &b.pages[i] }

// ReplacePage overwrites page i.
func (b *Builder) ReplacePage(i int, spec PageSpec) { b.pages[i] = spec }

// Build serializes every page in order.
func (b *Builder) Build() []byte {
	var out []byte
	for i, spec := range b.pages {
		segments := spec.Segs
		body := spec.Body
		if segments == nil {
			segments, body = packPackets(spec.Packets)
		}
		seq := spec.Sequence
		if seq == 0 {
			seq = i
		}
		serial := spec.Serial
		if serial == 0 {
			serial = b.Serial
		}
		out = append(out, serializePage(serial, uint32(seq), spec.Granule,
			headerType(spec), segments, body, spec.CorruptCRC, spec.BadCapture)...)
	}
	return out
}

func headerType(spec PageSpec) byte {
	var h byte
	if spec.Continued {
		h |= oggpage.FlagContinued
	}
	if spec.BOS {
		h |= oggpage.FlagBOS
	}
	if spec.EOS {
		h |= oggpage.FlagEOS
	}
	return h
}

// SegsClose returns the lacing values for a packet of n bytes that ends on
// the page (final lacing value below 255).
func SegsClose(n int) []byte {
	var segs []byte
	for n >= 255 {
		segs = append(segs, 255)
		n -= 255
	}
	return append(segs, byte(n))
}

// SegsOpen returns the lacing values for a prefix of n bytes of a packet
// that continues onto a later page. Ogg only opens a packet at a 255-byte
// boundary, so n must be a positive multiple of 255.
func SegsOpen(n int) []byte {
	if n <= 0 || n%255 != 0 {
		panic("testogg: open prefix must be a positive multiple of 255 bytes")
	}
	segs := make([]byte, n/255)
	for i := range segs {
		segs[i] = 255
	}
	return segs
}

// OpusHead builds a standard 19-byte OpusHead for mapping family 0.
func OpusHead(channels int, preskip uint16) []byte {
	p := make([]byte, 19)
	copy(p[0:8], []byte("OpusHead"))
	p[8] = 1
	p[9] = byte(channels)
	binary.LittleEndian.PutUint16(p[10:12], preskip)
	binary.LittleEndian.PutUint32(p[12:16], 48000)
	p[18] = 0
	return p
}

// OpusTags builds a minimal but legal comment header.
func OpusTags() []byte {
	var p []byte
	p = append(p, []byte("OpusTags")...)
	vendor := []byte("testogg")
	p = append(p, u32(uint32(len(vendor)))...)
	p = append(p, vendor...)
	p = append(p, u32(0)...) // zero user comments
	return p
}

// AudioPacket builds a code-0 Opus packet with the given TOC configuration
// number and dataLen payload bytes.
func AudioPacket(config int, stereo bool, dataLen int) []byte {
	toc := byte(config) << 3
	if stereo {
		toc |= 0x04
	}
	p := []byte{toc}
	for i := 0; i < dataLen; i++ {
		p = append(p, byte(i))
	}
	return p
}

// Code1Packet builds a code 1 packet with two equal-size frames.
func Code1Packet(config int, frameLen int) []byte {
	toc := byte(config)<<3 | 0x01
	p := []byte{toc}
	for f := 0; f < 2; f++ {
		for i := 0; i < frameLen; i++ {
			p = append(p, byte(f*7+i))
		}
	}
	return p
}

// Code3CBR builds a code 3 CBR packet with m frames of frameLen bytes.
func Code3CBR(config int, m int, frameLen int) []byte {
	toc := byte(config)<<3 | 0x03
	p := []byte{toc, byte(m << 2)} // v=0, p=0
	for f := 0; f < m; f++ {
		for i := 0; i < frameLen; i++ {
			p = append(p, byte(f*13+i))
		}
	}
	return p
}

// packPackets turns whole packets into a segment table plus contiguous body.
func packPackets(packets [][]byte) ([]byte, []byte) {
	var segs, body []byte
	for _, pkt := range packets {
		body = append(body, pkt...)
		segs = append(segs, SegsClose(len(pkt))...)
	}
	return segs, body
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

// serializePage builds one Ogg page and computes its CRC.
func serializePage(serial, seq uint32, granule int64, headerType byte,
	segments, body []byte, corruptCRC, badCapture bool) []byte {

	page := make([]byte, 27+len(segments)+len(body))
	copy(page[0:4], []byte("OggS"))
	page[4] = 0
	page[5] = headerType
	binary.LittleEndian.PutUint64(page[6:14], uint64(granule))
	binary.LittleEndian.PutUint32(page[14:18], serial)
	binary.LittleEndian.PutUint32(page[18:22], seq)
	// CRC field at 22:26 is left zero while the checksum is computed.
	page[26] = byte(len(segments))
	copy(page[27:27+len(segments)], segments)
	copy(page[27+len(segments):], body)

	crc := oggpage.CRC(page)
	binary.LittleEndian.PutUint32(page[22:26], crc)

	if corruptCRC && len(body) > 0 {
		page[27+len(segments)] ^= 0xff
	}
	if badCapture {
		page[0] = 'X'
	}
	return page
}
