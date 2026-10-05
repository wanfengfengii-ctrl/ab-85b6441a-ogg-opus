package audit

import (
	"errors"
	"os"
	"testing"

	"github.com/acoustics/opus-audit/internal/errs"
	"github.com/acoustics/opus-audit/internal/testogg"
)

// canonicalStream builds a valid mono stream with four audio packets spread
// over three audio pages, one of which (a 701-byte packet) spans a page
// boundary:
//
//	page 0 BOS   OpusHead
//	page 1       OpusTags
//	page 2       a1(960) a2(960)            granule 1920
//	page 3       first 510 B of big (open) granule -1
//	page 4 EOS   rest of big(960) a4(960)   granule 3840
func canonicalBuilder() *testogg.Builder {
	head := testogg.OpusHead(1, 312)
	tags := testogg.OpusTags()
	a1 := testogg.AudioPacket(19, false, 80) // 20 ms CELT = 960 samples
	a2 := testogg.AudioPacket(19, false, 81)
	big := testogg.AudioPacket(19, false, 700) // 701 bytes total
	a4 := testogg.AudioPacket(19, false, 82)

	b := testogg.New()
	b.Page(testogg.PageSpec{BOS: true, Granule: 0, Packets: [][]byte{head}})
	b.Page(testogg.PageSpec{Granule: 0, Packets: [][]byte{tags}})
	b.Page(testogg.PageSpec{Granule: 1920, Packets: [][]byte{a1, a2}})
	b.Page(testogg.PageSpec{
		Granule: -1,
		Segs:    testogg.SegsOpen(510),
		Body:    big[:510],
	})
	b.Page(testogg.PageSpec{
		Continued: true,
		EOS:       true,
		Granule:   3840,
		Segs:      append(testogg.SegsClose(len(big)-510), testogg.SegsClose(len(a4))...),
		Body:      append(append([]byte{}, big[510:]...), a4...),
	})
	return b
}

func TestCanonicalStream(t *testing.T) {
	stats, err := Audit(canonicalBuilder().Build())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	want := Stats{Pages: 5, AudioPackets: 4, DecodedSamples: 3840, PlayableSamples: 3840}
	if stats != want {
		t.Fatalf("stats = %+v, want %+v", stats, want)
	}
}

func TestEmptyEOSPage(t *testing.T) {
	head := testogg.OpusHead(1, 312)
	tags := testogg.OpusTags()
	a1 := testogg.AudioPacket(19, false, 80)

	build := func(granule int64) []byte {
		b := testogg.New()
		b.Page(testogg.PageSpec{BOS: true, Granule: 0, Packets: [][]byte{head}})
		b.Page(testogg.PageSpec{Granule: 0, Packets: [][]byte{tags}})
		b.Page(testogg.PageSpec{Granule: 960, Packets: [][]byte{a1}})
		// EOS page that completes no packets: granule must equal the anchor.
		b.Page(testogg.PageSpec{EOS: true, Granule: granule, Segs: []byte{}, Body: []byte{}})
		return b.Build()
	}

	t.Run("granule equals previous anchor", func(t *testing.T) {
		stats, err := Audit(build(960))
		if err != nil {
			t.Fatalf("Audit: %v", err)
		}
		if stats.PlayableSamples != 960 || stats.AudioPackets != 1 {
			t.Fatalf("stats = %+v", stats)
		}
	})

	t.Run("granule trims before previous anchor", func(t *testing.T) {
		_, err := Audit(build(0))
		var ve *errs.Error
		if !errors.As(err, &ve) || ve.Code != errs.CodeEOSGranuleRange || ve.Page != 3 {
			t.Fatalf("err = %v, want %s at page 3", err, errs.CodeEOSGranuleRange)
		}
	})
}

func TestStableStatsAcrossRepeatedAudits(t *testing.T) {
	data := canonicalBuilder().Build()
	first, err := Audit(data)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	for i := 0; i < 5; i++ {
		got, err := Audit(data)
		if err != nil {
			t.Fatalf("Audit %d: %v", i, err)
		}
		if got != first {
			t.Fatalf("audit %d = %+v, want %+v", i, got, first)
		}
	}
}

func TestEOSTrim(t *testing.T) {
	b := canonicalBuilder()
	// The anchor before the EOS page is 1920 (page 2); packets complete to
	// 3840. Trimming to 3000 keeps part of the spanned packet's page.
	setGranule(b, 4, 3000)
	stats, err := Audit(b.Build())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if stats.PlayableSamples != 3000 || stats.DecodedSamples != 3840 {
		t.Fatalf("stats = %+v, want playable 3000 / decoded 3840", stats)
	}
}

func TestRealOpusEncFile(t *testing.T) {
	data, err := os.ReadFile("testdata/opusenc-real.opus")
	if err != nil {
		t.Skipf("testdata missing: %v", err)
	}
	stats, err := Audit(data)
	if err != nil {
		t.Fatalf("Audit of real opusenc file: %v", err)
	}
	if stats.Pages != 13 {
		t.Errorf("Pages = %d, want 13", stats.Pages)
	}
	if stats.DecodedSamples != 519360 {
		t.Errorf("DecodedSamples = %d, want 519360", stats.DecodedSamples)
	}
	if stats.PlayableSamples != 518712 {
		t.Errorf("PlayableSamples = %d, want 518712", stats.PlayableSamples)
	}
}

func TestRejections(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(b *testogg.Builder)
		wantCode string
		wantPage int
	}{
		{
			name:     "empty body",
			mutate:   func(b *testogg.Builder) {},
			wantCode: errs.CodeNoPages,
			wantPage: -1,
		},
		{
			name: "bad capture pattern",
			mutate: func(b *testogg.Builder) {
				setBadCapture(b, 2)
			},
			wantCode: errs.CodeCapturePattern,
			wantPage: 2,
		},
		{
			name: "bad crc",
			mutate: func(b *testogg.Builder) {
				setCorruptCRC(b, 2)
			},
			wantCode: errs.CodeBadCRC,
			wantPage: 2,
		},
		{
			name: "body truncated",
			mutate: func(b *testogg.Builder) {
				// Declare far more body bytes than exist in the whole stream.
				segs := make([]byte, 255)
				for i := range segs {
					segs[i] = 255 // 65025 declared body bytes
				}
				setPage(b, 2, testogg.PageSpec{
					Granule: 0,
					Segs:    segs,
					Body:    []byte{1, 2, 3},
				})
			},
			wantCode: errs.CodeBodyTruncated,
			wantPage: 2,
		},
		{
			name: "first page not bos",
			mutate: func(b *testogg.Builder) {
				setBOS(b, 0, false)
			},
			wantCode: errs.CodeBOSNotFirst,
			wantPage: 0,
		},
		{
			name: "bos on later page",
			mutate: func(b *testogg.Builder) {
				setBOS(b, 2, true)
			},
			wantCode: errs.CodeBOSAfterStart,
			wantPage: 2,
		},
		{
			name: "sequence gap",
			mutate: func(b *testogg.Builder) {
				setSequence(b, 3, 99)
			},
			wantCode: errs.CodeSequenceGap,
			wantPage: 3,
		},
		{
			name: "serial mismatch",
			mutate: func(b *testogg.Builder) {
				setSerial(b, 3, 1234)
			},
			wantCode: errs.CodeSerialMismatch,
			wantPage: 3,
		},
		{
			name: "page after eos",
			mutate: func(b *testogg.Builder) {
				setEOS(b, 2, true)
			},
			wantCode: errs.CodePagesAfterEOS,
			wantPage: 3,
		},
		{
			name: "missing eos",
			mutate: func(b *testogg.Builder) {
				setEOS(b, 4, false)
			},
			wantCode: errs.CodeEOSMissing,
			wantPage: 4,
		},
		{
			name: "open packet not continued",
			mutate: func(b *testogg.Builder) {
				setContinued(b, 4, false)
			},
			wantCode: errs.CodeOpenPacketNotContinued,
			wantPage: 4,
		},
		{
			name: "continued without open packet",
			mutate: func(b *testogg.Builder) {
				setContinued(b, 2, true)
			},
			wantCode: errs.CodeContinuedWithoutOpen,
			wantPage: 2,
		},
		{
			name: "packet not closed at eos",
			mutate: func(b *testogg.Builder) {
				// Carry the continuation but leave its final lacing at 255.
				big := testogg.AudioPacket(19, false, 700)
				tail := append(append([]byte{}, big[510:]...),
					make([]byte, 255-(len(big)-510))...)
				setPage(b, 4, testogg.PageSpec{
					Continued: true,
					EOS:       true,
					Granule:   3840,
					Segs:      []byte{255},
					Body:      tail,
				})
			},
			wantCode: errs.CodePacketNotClosed,
			wantPage: 4,
		},
		{
			name: "first packet not opushead",
			mutate: func(b *testogg.Builder) {
				setPage(b, 0, testogg.PageSpec{
					BOS:     true,
					Granule: 0,
					Packets: [][]byte{[]byte("NotAHead...........")},
				})
			},
			wantCode: errs.CodeFirstPacketNotHead,
			wantPage: 0,
		},
		{
			name: "second packet not opustags",
			mutate: func(b *testogg.Builder) {
				setPage(b, 1, testogg.PageSpec{
					Granule: 0,
					Packets: [][]byte{[]byte("NotTags!!")},
				})
			},
			wantCode: errs.CodeSecondPacketNotTags,
			wantPage: 1,
		},
		{
			name: "opushead bad version",
			mutate: func(b *testogg.Builder) {
				head := testogg.OpusHead(1, 312)
				head[8] = 2
				setPage(b, 0, testogg.PageSpec{
					BOS:     true,
					Granule: 0,
					Packets: [][]byte{head},
				})
			},
			wantCode: errs.CodeOpusHeadMalformed,
			wantPage: 0,
		},
		{
			name: "header granule not zero",
			mutate: func(b *testogg.Builder) {
				setGranule(b, 1, 7)
			},
			wantCode: errs.CodeHeaderGranuleNotZero,
			wantPage: 1,
		},
		{
			name: "spanning page needs minus one",
			mutate: func(b *testogg.Builder) {
				setGranule(b, 3, 0)
			},
			wantCode: errs.CodeGranuleMustBeMinusOne,
			wantPage: 3,
		},
		{
			name: "audio page granule mismatch",
			mutate: func(b *testogg.Builder) {
				setGranule(b, 2, 1000)
			},
			wantCode: errs.CodeGranuleMismatch,
			wantPage: 2,
		},
		{
			name: "eos granule below previous anchor",
			mutate: func(b *testogg.Builder) {
				setGranule(b, 4, 1000)
			},
			wantCode: errs.CodeEOSGranuleRange,
			wantPage: 4,
		},
		{
			name: "eos granule above cumulative",
			mutate: func(b *testogg.Builder) {
				setGranule(b, 4, 999999)
			},
			wantCode: errs.CodeEOSGranuleRange,
			wantPage: 4,
		},
		{
			name: "empty audio packet",
			mutate: func(b *testogg.Builder) {
				a1 := []byte{}
				a2 := testogg.AudioPacket(19, false, 81)
				setPage(b, 2, testogg.PageSpec{
					Granule: 960,
					Packets: [][]byte{a1, a2},
				})
			},
			wantCode: errs.CodeEmptyAudioPacket,
			wantPage: 2,
		},
		{
			name: "malformed audio packet framing",
			mutate: func(b *testogg.Builder) {
				a1 := []byte{0x01, 0xaa} // code 1 with odd payload byte
				a2 := testogg.AudioPacket(19, false, 81)
				setPage(b, 2, testogg.PageSpec{
					Granule: 1920,
					Packets: [][]byte{a1, a2},
				})
			},
			wantCode: errs.CodePacketFraming,
			wantPage: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var data []byte
			if tc.wantCode == errs.CodeNoPages {
				data = nil
			} else {
				b := canonicalBuilder()
				tc.mutate(b)
				data = b.Build()
			}
			_, err := Audit(data)
			var ve *errs.Error
			if !errors.As(err, &ve) {
				t.Fatalf("Audit error = %v, want *errs.Error with code %q", err, tc.wantCode)
			}
			if ve.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", ve.Code, tc.wantCode)
			}
			if ve.Page != tc.wantPage {
				t.Errorf("page = %d, want %d", ve.Page, tc.wantPage)
			}
		})
	}
}

func TestTooManyPages(t *testing.T) {
	b := testogg.New()
	for i := 0; i < 2049; i++ {
		b.Page(testogg.PageSpec{
			BOS:     i == 0,
			Packets: [][]byte{{1, 2, 3}},
		})
	}
	_, err := Audit(b.Build())
	var ve *errs.Error
	if !errors.As(err, &ve) {
		t.Fatalf("Audit error = %v", err)
	}
	if ve.Code != errs.CodeTooManyPages {
		t.Fatalf("code = %q, want %q", ve.Code, errs.CodeTooManyPages)
	}
	if ve.Page != 2048 {
		t.Fatalf("page = %d, want 2048", ve.Page)
	}
}

func TestFirstFailurePageIndex(t *testing.T) {
	// Corrupt page 3 while page 4 is also wrong: the reported page must be
	// the first one that proves the stream invalid.
	b := canonicalBuilder()
	setCorruptCRC(b, 3)
	setGranule(b, 4, 0)
	_, err := Audit(b.Build())
	var ve *errs.Error
	if !errors.As(err, &ve) {
		t.Fatalf("Audit error = %v", err)
	}
	if ve.Page != 3 || ve.Code != errs.CodeBadCRC {
		t.Fatalf("got (%q, %d), want (OGG_BAD_CRC, 3)", ve.Code, ve.Page)
	}
}

// --- builder mutators -----------------------------------------------------

func specAt(b *testogg.Builder, i int) *testogg.PageSpec {
	return b.Spec(i)
}

func setPage(b *testogg.Builder, i int, spec testogg.PageSpec) { b.ReplacePage(i, spec) }
func setGranule(b *testogg.Builder, i int, g int64)            { specAt(b, i).Granule = g }
func setBOS(b *testogg.Builder, i int, v bool)                 { specAt(b, i).BOS = v }
func setEOS(b *testogg.Builder, i int, v bool)                 { specAt(b, i).EOS = v }
func setContinued(b *testogg.Builder, i int, v bool)           { specAt(b, i).Continued = v }
func setSequence(b *testogg.Builder, i int, v int)             { specAt(b, i).Sequence = v }
func setSerial(b *testogg.Builder, i int, v uint32)            { specAt(b, i).Serial = v }
func setCorruptCRC(b *testogg.Builder, i int)                  { specAt(b, i).CorruptCRC = true }
func setBadCapture(b *testogg.Builder, i int)                  { specAt(b, i).BadCapture = true }
