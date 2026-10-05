// Package opuspkt inspects Opus packets (RFC 6716): it derives the
// 48 kHz sample count from the TOC byte and frame-count framing and
// rejects packets whose internal framing is truncated or inconsistent.
package opuspkt

import (
	"github.com/acoustics/opus-audit/internal/errs"
)

// MaxFrameBytes is the largest legal compressed size of a single Opus
// frame (RFC 6716 section 3.2.1).
const MaxFrameBytes = 1275

// MaxPacketSamples is the maximum total duration of one Opus packet:
// 120 ms at 48 kHz (RFC 6716 section 3.2.5).
const MaxPacketSamples = 5760

// frameSamples maps the 32 TOC configurations to the per-frame sample
// count at 48 kHz (RFC 6716, Table 2):
//
//	configs 0..11  SILK-only: 10/20/40/60 ms per group of four
//	configs 12..15 Hybrid:    10/20 ms (12,13) and 10/20 ms (14,15)
//	configs 16..31 CELT-only: 2.5/5/10/20 ms per group of four
var frameSamples = [32]int{
	480, 960, 1920, 2880, // 0..3   SILK NB
	480, 960, 1920, 2880, // 4..7   SILK MB
	480, 960, 1920, 2880, // 8..11  SILK WB
	480, 960, // 12..13 Hybrid FB
	480, 960, // 14..15 Hybrid FB
	120, 240, 480, 960, // 16..19 CELT NB
	120, 240, 480, 960, // 20..23 CELT WB
	120, 240, 480, 960, // 24..27 CELT SWB
	120, 240, 480, 960, // 28..31 CELT FB
}

// Info describes a validated Opus packet.
type Info struct {
	Config       int
	Stereo       bool
	Code         int // TOC "c" field: 0..3
	Frames       int // number of frames in the packet
	FrameSamples int // samples per frame at 48 kHz
	Samples      int // total decodable samples at 48 kHz
}

// Inspect validates the TOC byte and the frame packing of a complete
// Opus packet and returns its 48 kHz sample count. The packet must be
// non-empty; the caller is responsible for rejecting zero-octet packets.
func Inspect(packet []byte) (Info, error) {
	if len(packet) == 0 {
		// Defensive: callers reject this with a dedicated code, but keep
		// the semantics explicit.
		return Info{}, errs.New(errs.CodeEmptyAudioPacket, -1, "zero-octet audio packet")
	}
	toc := packet[0]
	info := Info{
		Config: int(toc >> 3),
		Stereo: toc&0x04 != 0,
		Code:   int(toc & 0x03),
	}
	// config is a 5-bit field indexing the full table, so it is always in
	// range; the lookup documents the mapping.
	info.FrameSamples = frameSamples[info.Config]

	switch info.Code {
	case 0:
		info.Frames = 1
		if len(packet)-1 > MaxFrameBytes {
			return Info{}, errs.New(errs.CodePacketTooLong, -1,
				"code 0 frame is %d bytes, maximum is %d", len(packet)-1, MaxFrameBytes)
		}
	case 1:
		info.Frames = 2
		payload := len(packet) - 1
		if payload%2 != 0 {
			return Info{}, errs.New(errs.CodePacketFraming, -1,
				"code 1 packet has %d payload bytes, must be even", payload)
		}
		if payload/2 > MaxFrameBytes {
			return Info{}, errs.New(errs.CodePacketTooLong, -1,
				"code 1 frame is %d bytes, maximum is %d", payload/2, MaxFrameBytes)
		}
	case 2:
		info.Frames = 2
		n1, hdr, ok := readFrameLength(packet, 1)
		if !ok {
			return Info{}, errs.New(errs.CodePacketFraming, -1,
				"code 2 packet is too short to encode the first frame length")
		}
		remaining := len(packet) - 1 - hdr
		if n1 > remaining {
			return Info{}, errs.New(errs.CodePacketFraming, -1,
				"code 2 first frame claims %d bytes but only %d remain", n1, remaining)
		}
		if n1 > MaxFrameBytes || remaining-n1 > MaxFrameBytes {
			return Info{}, errs.New(errs.CodePacketTooLong, -1,
				"code 2 frame exceeds %d bytes", MaxFrameBytes)
		}
	case 3:
		if len(packet) < 2 {
			return Info{}, errs.New(errs.CodePacketFraming, -1,
				"code 3 packet must have at least 2 bytes")
		}
		b := packet[1]
		vbr := b&0x01 != 0
		padded := b&0x02 != 0
		m := int(b >> 2)
		if m == 0 {
			return Info{}, errs.New(errs.CodePacketFraming, -1,
				"code 3 frame count M must not be zero")
		}
		info.Frames = m

		pos := 2
		trailingPad := 0
		if padded {
			padBytes, padHeader, ok := readPadding(packet[pos:])
			if !ok {
				return Info{}, errs.New(errs.CodePacketFraming, -1,
					"code 3 padding length runs past the packet")
			}
			pos += padHeader
			// Padding bytes are appended at the very end of the packet.
			if pos+padBytes > len(packet) {
				return Info{}, errs.New(errs.CodePacketFraming, -1,
					"code 3 padding of %d bytes plus %d header bytes exceeds packet size %d",
					padBytes, padHeader, len(packet))
			}
			trailingPad = padBytes
		}
		frameEnd := len(packet) - trailingPad
		dataLen := frameEnd - pos
		if vbr {
			cursor := pos
			for f := 0; f < m-1; f++ {
				n, hdr, ok := readFrameLength(packet, cursor)
				if !ok {
					return Info{}, errs.New(errs.CodePacketFraming, -1,
						"code 3 VBR frame length %d runs past the packet", f)
				}
				if n > MaxFrameBytes {
					return Info{}, errs.New(errs.CodePacketTooLong, -1,
						"code 3 frame exceeds %d bytes", MaxFrameBytes)
				}
				cursor += hdr + n
				if cursor > frameEnd {
					return Info{}, errs.New(errs.CodePacketFraming, -1,
						"code 3 VBR frame %d data overruns the packet", f)
				}
			}
			last := frameEnd - cursor
			if last < 0 {
				return Info{}, errs.New(errs.CodePacketFraming, -1,
					"code 3 VBR frame data overruns the packet")
			}
			if last > MaxFrameBytes {
				return Info{}, errs.New(errs.CodePacketTooLong, -1,
					"code 3 final frame is %d bytes, maximum is %d",
					last, MaxFrameBytes)
			}
		} else {
			if dataLen%m != 0 {
				return Info{}, errs.New(errs.CodePacketFraming, -1,
					"code 3 CBR packet has %d frame bytes for %d frames, must divide evenly",
					dataLen, m)
			}
			if dataLen/m > MaxFrameBytes {
				return Info{}, errs.New(errs.CodePacketTooLong, -1,
					"code 3 CBR frame is %d bytes, maximum is %d",
					dataLen/m, MaxFrameBytes)
			}
		}
	}

	info.Samples = info.Frames * info.FrameSamples
	if info.Samples > MaxPacketSamples {
		return Info{}, errs.New(errs.CodePacketDuration, -1,
			"packet duration is %d samples (%d ms), exceeds 120 ms",
			info.Samples, info.Samples*1000/48000)
	}
	return info, nil
}

// readFrameLength decodes the 1- or 2-byte self-delimiting frame length
// (RFC 6716 section 3.2.1) at packet[start:]. It returns the length, the
// number of header bytes consumed, and ok=false if the bytes are missing.
func readFrameLength(packet []byte, start int) (length, consumed int, ok bool) {
	if start >= len(packet) {
		return 0, 0, false
	}
	first := packet[start]
	if first < 252 {
		return int(first), 1, true
	}
	if start+1 >= len(packet) {
		return 0, 0, false
	}
	return int(packet[start+1])*4 + int(first), 2, true
}

// readPadding parses Opus padding from the bytes following the frame
// count byte. It returns the number of trailing padding bytes, the number
// of bytes consumed by the padding-length encoding, and ok=false if the
// encoding runs past p.
func readPadding(p []byte) (padBytes, consumed int, ok bool) {
	for {
		if consumed >= len(p) {
			return 0, 0, false
		}
		v := p[consumed]
		consumed++
		if v == 255 {
			padBytes += 254
			continue
		}
		padBytes += int(v)
		return padBytes, consumed, true
	}
}
