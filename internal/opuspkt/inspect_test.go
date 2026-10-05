package opuspkt

import (
	"errors"
	"testing"

	"github.com/acoustics/opus-audit/internal/errs"
)

func errCode(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestInspectTOC(t *testing.T) {
	cases := []struct {
		name    string
		packet  []byte
		samples int
		frames  int
		code    int
	}{
		{"silk-20ms", []byte{0x08}, 960, 1, 0},        // config 1, code 0
		{"celt-2p5ms", []byte{0x80}, 120, 1, 0},       // config 16, code 0
		{"celt-20ms-stereo", []byte{0xfc}, 960, 1, 0}, // config 31, s=1, code 0
		{"code1-celt", Code1(19, 50), 960 * 2, 2, 1},
		{"code3-cbr-6x10ms", Code3CBR(18, 6, 10), 480 * 6, 6, 3},
		{"code3-vbr", code3VBR(18, [][]byte{{1, 2, 3}, {4, 5}, {6}}), 480 * 3, 3, 3},
		{"dtx-zero-frame-code2", []byte{0x0a, 0x00}, 960 * 2, 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Inspect(tc.packet)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if info.Samples != tc.samples {
				t.Errorf("Samples = %d, want %d", info.Samples, tc.samples)
			}
			if info.Frames != tc.frames {
				t.Errorf("Frames = %d, want %d", info.Frames, tc.frames)
			}
			if info.Code != tc.code {
				t.Errorf("Code = %d, want %d", info.Code, tc.code)
			}
		})
	}
}

func TestInspectErrors(t *testing.T) {
	cases := []struct {
		name   string
		packet []byte
		code   string
	}{
		{"empty", nil, "OPUS_EMPTY_AUDIO_PACKET"},
		{"code1-odd", []byte{0x09, 0xaa}, "OPUS_PACKET_FRAMING_INVALID"},
		{"code2-too-short", []byte{0x0a}, "OPUS_PACKET_FRAMING_INVALID"},
		{"code2-length-255-needs-second", []byte{0x0a, 253}, "OPUS_PACKET_FRAMING_INVALID"},
		{"code2-n1-overrun", []byte{0x0a, 10, 1, 2}, "OPUS_PACKET_FRAMING_INVALID"},
		{"code3-one-byte", []byte{0x0b}, "OPUS_PACKET_FRAMING_INVALID"},
		{"code3-zero-frames", []byte{0x0b, 0x00}, "OPUS_PACKET_FRAMING_INVALID"},
		{"code3-cbr-remainder", []byte{0x0b, 0x08, 1, 2, 3}, "OPUS_PACKET_FRAMING_INVALID"},
		{"code3-duration-overflow", func() []byte {
			p := []byte{0x0b, 48 << 2} // 48 x 10 ms = 480 ms
			return append(p, make([]byte, 48)...)
		}(), "OPUS_PACKET_DURATION_TOO_LONG"},
		{"frame-too-long", append([]byte{0x80}, make([]byte, 1276)...), "OPUS_FRAME_TOO_LONG"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Inspect(tc.packet)
			if err == nil {
				t.Fatalf("Inspect unexpectedly succeeded")
			}
			if got := errCode(err); got != tc.code {
				t.Fatalf("error code = %q, want %q (%v)", got, tc.code, err)
			}
		})
	}
}

// Code1 builds a code 1 packet with two equal-size frames for tests.
func Code1(config, frameLen int) []byte {
	toc := byte(config)<<3 | 0x01
	p := []byte{toc}
	for f := 0; f < 2; f++ {
		for i := 0; i < frameLen; i++ {
			p = append(p, byte(f*7+i))
		}
	}
	return p
}

// Code3CBR builds a code 3 CBR packet.
func Code3CBR(config, m, frameLen int) []byte {
	toc := byte(config)<<3 | 0x03
	p := []byte{toc, byte(m << 2)}
	for f := 0; f < m; f++ {
		for i := 0; i < frameLen; i++ {
			p = append(p, byte(f*13+i))
		}
	}
	return p
}

// code3VBR builds a code 3 VBR packet from explicit frame payloads.
func code3VBR(config int, frames [][]byte) []byte {
	toc := byte(config)<<3 | 0x03
	p := []byte{toc, byte(len(frames)<<2 | 0x01)}
	for _, fr := range frames[:len(frames)-1] {
		p = append(p, byte(len(fr)))
	}
	for _, fr := range frames {
		p = append(p, fr...)
	}
	return p
}

// code3VBRPadded builds a code 3 VBR packet whose trailing bytes are Opus
// padding rather than frame data.
func code3VBRPadded(config int, frames [][]byte, padLen int) []byte {
	toc := byte(config)<<3 | 0x03
	p := []byte{toc, byte(len(frames)<<2 | 0x02 | 0x01)}
	if padLen <= 254 {
		p = append(p, byte(padLen))
	} else {
		rem := padLen - 254
		p = append(p, 255, byte(rem))
	}
	for _, fr := range frames[:len(frames)-1] {
		p = append(p, byte(len(fr)))
	}
	for _, fr := range frames {
		p = append(p, fr...)
	}
	p = append(p, make([]byte, padLen)...)
	return p
}

func TestCode3Padding(t *testing.T) {
	// Two frames of 3 and 2 bytes, plus 5 trailing padding bytes. Without
	// subtracting the padding the last frame would be measured as 7 bytes.
	pkt := code3VBRPadded(18, [][]byte{{1, 2, 3}, {4, 5}}, 5)
	info, err := Inspect(pkt)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.Frames != 2 || info.Samples != 960 {
		t.Fatalf("info = %+v, want 2 frames / 960 samples", info)
	}

	// Padding length encoding that overruns the packet must be rejected.
	bad := []byte{0x0b, 0x02, 10} // claims 10 padding bytes, none present
	if _, err := Inspect(bad); err == nil || errCode(err) != "OPUS_PACKET_FRAMING_INVALID" {
		t.Fatalf("bad padding err = %v, want OPUS_PACKET_FRAMING_INVALID", err)
	}
}
