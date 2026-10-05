// Package audit implements the end-to-end validation of a single logical
// Ogg Opus byte stream: Ogg container integrity, Opus header identification,
// per-packet TOC sample accounting, and granule-position timeline checks.
package audit

import (
	"encoding/binary"

	"github.com/acoustics/opus-audit/internal/errs"
	"github.com/acoustics/opus-audit/internal/oggpage"
	"github.com/acoustics/opus-audit/internal/opuspkt"
)

// MaxPages is the hard cap on physical Ogg pages in an audited stream.
const MaxPages = 2048

var (
	opusHeadMagic = [8]byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd'}
	opusTagsMagic = [8]byte{'O', 'p', 'u', 's', 'T', 'a', 'g', 's'}
)

// Stats is the stable, deterministic result of a successful audit.
type Stats struct {
	// Pages is the number of physical Ogg pages in the stream.
	Pages int `json:"pages"`
	// AudioPackets is the number of complete Opus audio data packets
	// (everything after the OpusHead and OpusTags header packets).
	AudioPackets int64 `json:"audio_packets"`
	// DecodedSamples is the sum of the 48 kHz sample counts of every
	// complete audio packet, derived from their TOC sequences.
	DecodedSamples int64 `json:"decoded_samples"`
	// PlayableSamples is the number of samples on the playable timeline:
	// the EOS page granule position, reflecting any end trimming.
	PlayableSamples int64 `json:"playable_samples"`
}

// headConfig holds the parsed OpusHead fields the auditor relies on.
type headConfig struct {
	channels int
	preskip  uint16
}

// Audit fully validates one Ogg Opus stream and returns its statistics.
// The returned *errs.Error is nil on success and otherwise attributes the
// failure to the first page index that proves the stream invalid.
func Audit(data []byte) (Stats, error) {
	if len(data) == 0 {
		return Stats{}, errs.New(errs.CodeNoPages, -1, "empty body: no Ogg pages present")
	}

	pages, err := parsePages(data)
	if err != nil {
		return Stats{}, err
	}
	if err := validateContainer(pages); err != nil {
		return Stats{}, err
	}
	return validateTimeline(pages)
}

// parsePages walks the byte stream and parses every physical page.
func parsePages(data []byte) ([]*oggpage.Page, error) {
	pages := make([]*oggpage.Page, 0, 64)
	off := 0
	for off < len(data) {
		if len(pages) >= MaxPages {
			return nil, errs.New(errs.CodeTooManyPages, len(pages),
				"stream exceeds the limit of %d Ogg pages", MaxPages)
		}
		p, next, perr := oggpage.Parse(data, off, len(pages))
		if perr != nil {
			return nil, perr
		}
		pages = append(pages, p)
		off = next
	}
	return pages, nil
}

// validateContainer checks properties that can be decided without packet
// reassembly: BOS/EOS placement, serial number, sequence continuity, and
// the consistency of the cross-page continued-packet flags.
func validateContainer(pages []*oggpage.Page) error {
	var serial uint32
	prevOpen := false
	eosSeen := false
	for i, p := range pages {
		if i == 0 {
			serial = p.Serial
			if !p.BOS() {
				return errs.New(errs.CodeBOSNotFirst, i,
					"first page must have the beginning-of-stream flag set")
			}
			if p.Sequence != 0 {
				return errs.New(errs.CodeSequenceGap, i,
					"first page sequence number is %d, expected 0", p.Sequence)
			}
		} else {
			if p.Serial != serial {
				return errs.New(errs.CodeSerialMismatch, i,
					"serial number changes from 0x%08x to 0x%08x", serial, p.Serial)
			}
			if p.Sequence != uint32(i) {
				return errs.New(errs.CodeSequenceGap, i,
					"page sequence number is %d, expected %d", p.Sequence, i)
			}
			if p.BOS() {
				return errs.New(errs.CodeBOSAfterStart, i,
					"beginning-of-stream flag set on a non-first page")
			}
		}
		if eosSeen {
			return errs.New(errs.CodePagesAfterEOS, i,
				"page follows a page already marked end-of-stream")
		}

		// Continued-packet flag consistency. A page opens a packet when
		// its final lacing value is 255; the next page must continue it.
		if p.Continued() != prevOpen {
			if p.Continued() {
				return errs.New(errs.CodeContinuedWithoutOpen, i,
					"page claims a continued packet but the previous page does not end with an open packet")
			}
			return errs.New(errs.CodeOpenPacketNotContinued, i,
				"previous page ends with an open packet but this page does not set the continued flag")
		}
		prevOpen = p.OpensPacket()
		if p.EOS() {
			eosSeen = true
		}
	}

	last := pages[len(pages)-1]
	if !last.EOS() {
		return errs.New(errs.CodeEOSMissing, last.Index,
			"stream is not terminated by an end-of-stream page")
	}
	if prevOpen {
		return errs.New(errs.CodePacketNotClosed, last.Index,
			"final page leaves a packet spanning past end-of-stream")
	}
	return nil
}

// validateTimeline reassembles packets, identifies the mandatory headers,
// accounts audio samples, and enforces the granule-position rules.
func validateTimeline(pages []*oggpage.Page) (Stats, error) {
	var (
		pending      []byte
		ordinal      int
		head         headConfig
		haveHead     bool
		cumulative   int64 // samples of all complete audio packets
		anchor       int64 // granule of the previous page with completed audio packets
		audioPackets int64
		playable     int64
	)

	for _, p := range pages {
		audioBefore := audioPackets
		var (
			pageAudioPackets int
			headerCompleted  bool
		)
		bodyPos := 0
		cur := pending
		for _, v := range p.Segments {
			cur = append(cur, p.Body[bodyPos:bodyPos+int(v)]...)
			bodyPos += int(v)
			if v < 255 {
				// A complete packet ends on this page.
				switch {
				case ordinal == 0:
					headerCompleted = true
					cfg, err := parseOpusHead(cur)
					if err != nil {
						return Stats{}, repage(err, p.Index)
					}
					head = cfg
					haveHead = true
				case ordinal == 1:
					headerCompleted = true
					if !haveHead {
						// Defensive: ordinal 0 would already have failed.
						return Stats{}, errs.New(errs.CodeFirstPacketNotHead, p.Index,
							"OpusTags packet appears without a preceding OpusHead")
					}
					if len(cur) < len(opusTagsMagic) {
						return Stats{}, errs.New(errs.CodeSecondPacketNotTags, p.Index,
							"second packet is %d bytes, too short to be OpusTags", len(cur))
					}
					var magic [8]byte
					copy(magic[:], cur[:8])
					if magic != opusTagsMagic {
						return Stats{}, errs.New(errs.CodeSecondPacketNotTags, p.Index,
							"second packet does not begin with 'OpusTags'")
					}
					if err := validateOpusTags(cur); err != nil {
						return Stats{}, repage(err, p.Index)
					}
				default:
					info, err := inspectAudio(cur)
					if err != nil {
						return Stats{}, repage(err, p.Index)
					}
					cumulative += int64(info.Samples)
					pageAudioPackets++
					audioPackets++
				}
				ordinal++
				cur = nil
			}
		}
		pending = cur

		if headerCompleted && pageAudioPackets > 0 {
			return Stats{}, errs.New(errs.CodeAudioOnHeaderPage, p.Index,
				"audio packets complete on the same page as an Opus header packet")
		}

		// Granule position validation for this page.
		switch {
		case p.EOS():
			// End trimming: the EOS granule may cut the tail, but it may
			// not move before the previous anchored granule or claim more
			// samples than the packets actually decode to.
			g := p.Granule
			if g < anchor || g > cumulative {
				return Stats{}, errs.New(errs.CodeEOSGranuleRange, p.Index,
					"EOS granule %d is outside [%d, %d]", g, anchor, cumulative)
			}
			// RFC 7845 section 4.5: if the stream's first completed audio
			// packets already land on the EOS page, its granule must not be
			// below the pre-skip the decoder will discard.
			if audioBefore == 0 && pageAudioPackets > 0 && g < int64(head.preskip) {
				return Stats{}, errs.New(errs.CodeEOSGranuleRange, p.Index,
					"EOS granule %d is below the %d-sample pre-skip", g, head.preskip)
			}
			playable = g
			if pageAudioPackets > 0 {
				anchor = g
			}
		case pageAudioPackets > 0:
			if p.Granule != cumulative {
				return Stats{}, errs.New(errs.CodeGranuleMismatch, p.Index,
					"audio page granule is %d but packets complete to %d decoded samples",
					p.Granule, cumulative)
			}
			anchor = cumulative
		case headerCompleted:
			if p.Granule != 0 {
				return Stats{}, errs.New(errs.CodeHeaderGranuleNotZero, p.Index,
					"header page granule is %d, must be 0", p.Granule)
			}
		default:
			// A page wholly spanned by a packet that completes later has
			// no granule position.
			if p.Granule != -1 {
				return Stats{}, errs.New(errs.CodeGranuleMustBeMinusOne, p.Index,
					"page without a completed packet has granule %d, must be -1", p.Granule)
			}
		}
	}

	if audioPackets == 0 {
		return Stats{}, errs.New(errs.CodeEOSWithoutAudio, pages[len(pages)-1].Index,
			"stream ends without any complete audio data packet")
	}

	return Stats{
		Pages:           len(pages),
		AudioPackets:    audioPackets,
		DecodedSamples:  cumulative,
		PlayableSamples: playable,
	}, nil
}

// inspectAudio validates one complete audio packet and returns its TOC info.
func inspectAudio(packet []byte) (opuspkt.Info, error) {
	if len(packet) == 0 {
		return opuspkt.Info{}, errs.New(errs.CodeEmptyAudioPacket, -1,
			"zero-octet audio data packet")
	}
	return opuspkt.Inspect(packet)
}

// validateOpusTags performs a structural check of the comment header: the
// vendor string length prefix and string, followed by the user comment
// count and each comment's length/string, must all be reachable.
func validateOpusTags(pkt []byte) error {
	pos := len(opusTagsMagic)
	if pos+4 > len(pkt) {
		return errs.New(errs.CodeOpusTagsMalformed, -1,
			"OpusTags is %d bytes, missing the vendor length", len(pkt))
	}
	vendorLen := int(binary.LittleEndian.Uint32(pkt[pos : pos+4]))
	pos += 4
	if vendorLen > len(pkt)-pos {
		return errs.New(errs.CodeOpusTagsMalformed, -1,
			"OpusTags vendor string length %d overruns the packet", vendorLen)
	}
	pos += vendorLen

	if pos+4 > len(pkt) {
		return errs.New(errs.CodeOpusTagsMalformed, -1,
			"OpusTags is missing the user comment count")
	}
	nComments := int(binary.LittleEndian.Uint32(pkt[pos : pos+4]))
	pos += 4
	for i := 0; i < nComments; i++ {
		if pos+4 > len(pkt) {
			return errs.New(errs.CodeOpusTagsMalformed, -1,
				"OpusTags comment %d length overruns the packet", i)
		}
		n := int(binary.LittleEndian.Uint32(pkt[pos : pos+4]))
		pos += 4
		if n > len(pkt)-pos {
			return errs.New(errs.CodeOpusTagsMalformed, -1,
				"OpusTags comment %d (%d bytes) overruns the packet", i, n)
		}
		pos += n
	}
	return nil
}

// parseOpusHead validates the identification header per RFC 7845 section 5.1.
// The auditor supports channel mapping families 0 and 1.
func parseOpusHead(pkt []byte) (headConfig, error) {
	if len(pkt) < len(opusHeadMagic) {
		return headConfig{}, errs.New(errs.CodeFirstPacketNotHead, -1,
			"first packet is %d bytes, too short to be OpusHead", len(pkt))
	}
	var magic [8]byte
	copy(magic[:], pkt[:8])
	if magic != opusHeadMagic {
		return headConfig{}, errs.New(errs.CodeFirstPacketNotHead, -1,
			"first packet does not begin with 'OpusHead'")
	}
	if len(pkt) < 19 {
		return headConfig{}, errs.New(errs.CodeOpusHeadMalformed, -1,
			"OpusHead is %d bytes, need at least 19", len(pkt))
	}
	if pkt[8] != 1 {
		return headConfig{}, errs.New(errs.CodeOpusHeadMalformed, -1,
			"OpusHead version is %d, only version 1 is supported", pkt[8])
	}
	channels := int(pkt[9])
	if channels == 0 {
		return headConfig{}, errs.New(errs.CodeOpusHeadMalformed, -1,
			"OpusHead channel count must not be zero")
	}
	preskip := binary.LittleEndian.Uint16(pkt[10:12])
	cfg := headConfig{channels: channels, preskip: preskip}
	family := pkt[18]

	switch family {
	case 0:
		if channels > 2 {
			return headConfig{}, errs.New(errs.CodeUnsupportedMapping, -1,
				"mapping family 0 allows 1 or 2 channels, OpusHead declares %d", channels)
		}
		if len(pkt) != 19 {
			return headConfig{}, errs.New(errs.CodeOpusHeadMalformed, -1,
				"mapping family 0 must omit the channel mapping table, header is %d bytes", len(pkt))
		}
	case 1:
		if channels > 8 {
			return headConfig{}, errs.New(errs.CodeUnsupportedMapping, -1,
				"mapping family 1 allows at most 8 channels, OpusHead declares %d", channels)
		}
		if len(pkt) < 21+channels {
			return headConfig{}, errs.New(errs.CodeOpusHeadMalformed, -1,
				"mapping family 1 header is %d bytes, need at least %d", len(pkt), 21+channels)
		}
		n := int(pkt[19])
		m := int(pkt[20])
		if n == 0 {
			return headConfig{}, errs.New(errs.CodeOpusHeadMalformed, -1,
				"OpusHead stream count must not be zero")
		}
		if m > n {
			return headConfig{}, errs.New(errs.CodeOpusHeadMalformed, -1,
				"OpusHead coupled stream count %d exceeds stream count %d", m, n)
		}
		if m+n > 255 {
			return headConfig{}, errs.New(errs.CodeOpusHeadMalformed, -1,
				"OpusHead decoded channel count %d exceeds 255", m+n)
		}
		// The auditor derives a duration from a single TOC per Ogg packet,
		// so it supports only single-Opus-stream channel configurations.
		if n != 1 {
			return headConfig{}, errs.New(errs.CodeUnsupportedMapping, -1,
				"only a single encoded Opus stream is supported, OpusHead declares %d streams", n)
		}
		for c := 0; c < channels; c++ {
			idx := int(pkt[21+c])
			if idx != 255 && idx >= m+n {
				return headConfig{}, errs.New(errs.CodeOpusHeadMalformed, -1,
					"OpusHead channel mapping entry %d is out of range for %d decoded channels",
					idx, m+n)
			}
		}
	default:
		return headConfig{}, errs.New(errs.CodeUnsupportedMapping, -1,
			"unsupported Opus channel mapping family %d", family)
	}
	return cfg, nil
}

// repage attributes an error that originated while inspecting a packet to
// the page on which that packet completed.
func repage(err error, page int) error {
	if e, ok := err.(*errs.Error); ok {
		e.Page = page
		return e
	}
	return err
}
