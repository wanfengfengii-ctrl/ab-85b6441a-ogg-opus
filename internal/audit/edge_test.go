package audit

import (
	"errors"
	"testing"

	"github.com/acoustics/opus-audit/internal/errs"
	"github.com/acoustics/opus-audit/internal/testogg"
)

func TestBothHeadersOnBOSPage(t *testing.T) {
	head := testogg.OpusHead(1, 312)
	tags := testogg.OpusTags()
	a1 := testogg.AudioPacket(19, false, 40)
	b := testogg.New()
	b.Page(testogg.PageSpec{BOS: true, EOS: false, Granule: 0, Packets: [][]byte{head, tags}})
	b.Page(testogg.PageSpec{EOS: true, Granule: 960, Packets: [][]byte{a1}})
	stats, err := Audit(b.Build())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if stats.Pages != 2 || stats.AudioPackets != 1 || stats.PlayableSamples != 960 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestMultiplePacketsAndMultipleSpanningPages(t *testing.T) {
	head := testogg.OpusHead(1, 312)
	tags := testogg.OpusTags()
	// Three large packets, each forced to span via explicit segment tables.
	p1 := testogg.AudioPacket(19, false, 900) // 901 bytes
	p2 := testogg.AudioPacket(18, false, 900) // 480 samples
	p3 := testogg.AudioPacket(17, false, 900) // 240 samples

	b := testogg.New()
	b.Page(testogg.PageSpec{BOS: true, Granule: 0, Packets: [][]byte{head}})
	b.Page(testogg.PageSpec{Granule: 0, Packets: [][]byte{tags}})
	// p1 first 510 bytes, open, granule -1
	b.Page(testogg.PageSpec{Granule: -1, Segs: testogg.SegsOpen(510), Body: p1[:510]})
	// rest of p1 (391 bytes -> closes, +960), then first 255 of p2 open
	body := append(append([]byte{}, p1[510:]...), p2[:255]...)
	segs := append(testogg.SegsClose(len(p1)-510), testogg.SegsOpen(255)...)
	b.Page(testogg.PageSpec{Continued: true, Granule: 960, Segs: segs, Body: body})
	// rest of p2 (646 bytes: 255+255+136), close, then p3 fully
	body = append(append([]byte{}, p2[255:]...), p3...)
	segs = append(append([]byte{}, testogg.SegsClose(len(p2)-255)...), testogg.SegsClose(len(p3))...)
	b.Page(testogg.PageSpec{Continued: true, EOS: true, Granule: 960 + 480 + 240, Segs: segs, Body: body})

	stats, err := Audit(b.Build())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if stats.AudioPackets != 3 || stats.DecodedSamples != 1680 || stats.PlayableSamples != 1680 {
		t.Fatalf("stats = %+v, want packets 3 / decoded 1680 / playable 1680", stats)
	}
}

func TestFirstAudioPageIsEOSBelowPreskip(t *testing.T) {
	head := testogg.OpusHead(1, 312)
	tags := testogg.OpusTags()
	a1 := testogg.AudioPacket(19, false, 40) // 960 samples
	b := testogg.New()
	b.Page(testogg.PageSpec{BOS: true, Granule: 0, Packets: [][]byte{head}})
	b.Page(testogg.PageSpec{Granule: 0, Packets: [][]byte{tags}})
	// EOS granule 100 is below the 312-sample pre-skip: invalid per RFC 7845 4.5.
	b.Page(testogg.PageSpec{EOS: true, Granule: 100, Packets: [][]byte{a1}})
	_, err := Audit(b.Build())
	var ve *errs.Error
	if !errors.As(err, &ve) || ve.Code != errs.CodeEOSGranuleRange || ve.Page != 2 {
		t.Fatalf("err = %v, want %s at page 2", err, errs.CodeEOSGranuleRange)
	}
}

func TestFirstAudioPageIsEOSWithinPreskipAndCumulative(t *testing.T) {
	head := testogg.OpusHead(1, 312)
	tags := testogg.OpusTags()
	a1 := testogg.AudioPacket(19, false, 40)
	b := testogg.New()
	b.Page(testogg.PageSpec{BOS: true, Granule: 0, Packets: [][]byte{head}})
	b.Page(testogg.PageSpec{Granule: 0, Packets: [][]byte{tags}})
	// EOS granule 500 is within [preskip 312, cumulative 960]: legal trim.
	b.Page(testogg.PageSpec{EOS: true, Granule: 500, Packets: [][]byte{a1}})
	stats, err := Audit(b.Build())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if stats.PlayableSamples != 500 {
		t.Fatalf("playable = %d, want 500", stats.PlayableSamples)
	}
}

func TestCode2TwoByteFrameLength(t *testing.T) {
	head := testogg.OpusHead(1, 312)
	tags := testogg.OpusTags()
	// Code 2 (c=2 -> TOC low bits 10), first frame 300 bytes encoded with a
	// two-byte length (252..255 => second*4+first), second frame 20 bytes.
	firstLen := 300
	toc := byte(19)<<3 | 0x02
	// first=252, second=12 -> 12*4+252 = 300
	pkt := []byte{toc, 252, 12}
	pkt = append(pkt, make([]byte, firstLen)...)
	pkt = append(pkt, make([]byte, 20)...)

	b := testogg.New()
	b.Page(testogg.PageSpec{BOS: true, Granule: 0, Packets: [][]byte{head}})
	b.Page(testogg.PageSpec{Granule: 0, Packets: [][]byte{tags}})
	b.Page(testogg.PageSpec{EOS: true, Granule: 1920, Packets: [][]byte{pkt}})
	stats, err := Audit(b.Build())
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if stats.AudioPackets != 1 || stats.DecodedSamples != 1920 {
		t.Fatalf("stats = %+v, want 1 packet / 1920 samples", stats)
	}
}

func TestOpusTagsMalformed(t *testing.T) {
	head := testogg.OpusHead(1, 312)
	badTags := []byte("OpusTags")
	badTags = append(badTags, 0xff, 0xff, 0xff, 0x7f) // claims a huge vendor string
	b := testogg.New()
	b.Page(testogg.PageSpec{BOS: true, Granule: 0, Packets: [][]byte{head}})
	b.Page(testogg.PageSpec{EOS: true, Granule: 0, Packets: [][]byte{badTags}})
	_, err := Audit(b.Build())
	var ve *errs.Error
	if !errors.As(err, &ve) || ve.Code != errs.CodeOpusTagsMalformed || ve.Page != 1 {
		t.Fatalf("err = %v, want %s at page 1", err, errs.CodeOpusTagsMalformed)
	}
}
