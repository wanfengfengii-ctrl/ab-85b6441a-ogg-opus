// Package oggpage parses physical Ogg pages (RFC 3533) and reassembles the
// logical packet stream described by their segment tables.
package oggpage

import (
	"encoding/binary"

	"github.com/acoustics/opus-audit/internal/errs"
)

// Capture is the 4-byte Ogg capture pattern "OggS".
var Capture = [4]byte{'O', 'g', 'g', 'S'}

// Header type flag bits.
const (
	FlagContinued = 0x01
	FlagBOS       = 0x02
	FlagEOS       = 0x04
)

const (
	headerFixedSize = 27
	maxSegEntries   = 255
)

// Page is one fully validated physical Ogg page. Body holds the raw page
// body bytes exactly as carried by the segment table.
type Page struct {
	Index       int
	Version     byte
	HeaderType  byte
	Granule     int64
	Serial      uint32
	Sequence    uint32
	Segments    []byte // raw lacing values
	Body        []byte
	HeaderStart int // offset of capture pattern in the input
	HeaderEnd   int // offset just past the segment table
	BodyEnd     int // offset just past the body
}

// Continued reports the "continued packet" flag.
func (p *Page) Continued() bool { return p.HeaderType&FlagContinued != 0 }

// BOS reports the "beginning of stream" flag.
func (p *Page) BOS() bool { return p.HeaderType&FlagBOS != 0 }

// EOS reports the "end of stream" flag.
func (p *Page) EOS() bool { return p.HeaderType&FlagEOS != 0 }

// OpensPacket reports whether the page leaves a packet unfinished, i.e. its
// final lacing value is 255.
func (p *Page) OpensPacket() bool {
	return len(p.Segments) > 0 && p.Segments[len(p.Segments)-1] == 255
}

// crcTable is the Ogg generator-polynomial lookup table (0x04c11db7,
// non-reflected, init/xorout zero).
var crcTable [256]uint32

func init() {
	for i := 0; i < 256; i++ {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		crcTable[i] = r
	}
}

// CRC returns the Ogg CRC over a serialized page whose CRC field (bytes
// 22..25) is supplied as zero.
func CRC(page []byte) uint32 {
	return updateCRC(0, page)
}

// updateCRC folds one byte chunk into a running Ogg CRC.
func updateCRC(crc uint32, chunk []byte) uint32 {
	for _, b := range chunk {
		crc = (crc << 8) ^ crcTable[byte(crc>>24)^b]
	}
	return crc
}

// Parse extracts and validates a single Ogg page beginning at data[off].
// It verifies the capture pattern, version, header-type reserved bits,
// segment-table/body lengths, and the page CRC. It returns the parsed page
// and the offset just beyond it.
func Parse(data []byte, off, index int) (*Page, int, error) {
	if len(data)-off < headerFixedSize {
		if hasCaptureAt(data, off) {
			return nil, 0, errs.New(errs.CodePageTruncated, index,
				"page header is %d bytes short", headerFixedSize-(len(data)-off))
		}
		return nil, 0, errs.New(errs.CodeCapturePattern, index,
			"capture pattern 'OggS' not found at offset %d", off)
	}
	var capt [4]byte
	copy(capt[:], data[off:off+4])
	if capt != Capture {
		return nil, 0, errs.New(errs.CodeCapturePattern, index,
			"capture pattern 'OggS' not found at offset %d", off)
	}

	p := &Page{Index: index, HeaderStart: off}
	p.Version = data[off+4]
	if p.Version != 0 {
		return nil, 0, errs.New(errs.CodeBadVersion, index,
			"unsupported Ogg version %d (want 0)", p.Version)
	}
	p.HeaderType = data[off+5]
	if p.HeaderType&^uint8(FlagContinued|FlagBOS|FlagEOS) != 0 {
		return nil, 0, errs.New(errs.CodeBadHeaderType, index,
			"reserved header-type bits set: 0x%02x", p.HeaderType)
	}
	p.Granule = int64(binary.LittleEndian.Uint64(data[off+6 : off+14]))
	p.Serial = binary.LittleEndian.Uint32(data[off+14 : off+18])
	p.Sequence = binary.LittleEndian.Uint32(data[off+18 : off+22])

	nSeg := int(data[off+26])
	segEnd := off + headerFixedSize + nSeg
	if len(data) < segEnd {
		return nil, 0, errs.New(errs.CodePageTruncated, index,
			"segment table truncated: need %d bytes, have %d",
			segEnd-off, len(data)-off)
	}

	bodyLen := 0
	seg := make([]byte, nSeg)
	copy(seg, data[off+headerFixedSize:segEnd])
	for _, v := range seg {
		bodyLen += int(v)
	}
	bodyEnd := segEnd + bodyLen
	if len(data) < bodyEnd {
		return nil, 0, errs.New(errs.CodeBodyTruncated, index,
			"page body truncated: segment table declares %d bytes, %d present",
			bodyLen, len(data)-segEnd)
	}

	// CRC verification over the full serialized page. The 4 CRC bytes
	// participate in the checksum as zero (RFC 3533 section 4).
	stored := binary.LittleEndian.Uint32(data[off+22 : off+26])
	crc := updateCRC(0, data[off:off+22])
	crc = updateCRC(crc, []byte{0, 0, 0, 0})
	crc = updateCRC(crc, data[off+26:bodyEnd])
	if crc != stored {
		return nil, 0, errs.New(errs.CodeBadCRC, index,
			"CRC mismatch: page declares 0x%08x, computed 0x%08x", stored, crc)
	}

	p.Segments = seg
	p.Body = data[segEnd:bodyEnd]
	p.HeaderEnd = segEnd
	p.BodyEnd = bodyEnd
	return p, bodyEnd, nil
}

func hasCaptureAt(data []byte, off int) bool {
	if len(data)-off < 4 {
		return false
	}
	return data[off] == 'O' && data[off+1] == 'g' && data[off+2] == 'g' && data[off+3] == 'S'
}
