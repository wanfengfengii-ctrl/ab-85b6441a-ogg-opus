// Package errs defines the stable validation error codes shared by the
// Ogg/Opus audit layers. Every code is part of the HTTP API contract:
// clients may key on the string value, so do not rename existing codes.
package errs

import "fmt"

// Stable error codes returned by the auditor and surfaced as 422 responses.
const (
	// Ogg container level.
	CodeNoPages                = "OGG_NO_PAGES"
	CodeTooManyPages           = "OGG_TOO_MANY_PAGES"
	CodeCapturePattern         = "OGG_CAPTURE_PATTERN"
	CodePageTruncated          = "OGG_PAGE_TRUNCATED"
	CodeBodyTruncated          = "OGG_BODY_TRUNCATED"
	CodeBadCRC                 = "OGG_BAD_CRC"
	CodeBadVersion             = "OGG_BAD_VERSION"
	CodeBadHeaderType          = "OGG_BAD_HEADER_TYPE"
	CodeSerialMismatch         = "OGG_SERIAL_MISMATCH"
	CodeSequenceGap            = "OGG_SEQUENCE_GAP"
	CodeBOSNotFirst            = "OGG_BOS_NOT_FIRST"
	CodeBOSAfterStart          = "OGG_BOS_AFTER_START"
	CodePagesAfterEOS          = "OGG_PAGES_AFTER_EOS"
	CodeEOSMissing             = "OGG_EOS_MISSING"
	CodeContinuedWithoutOpen   = "OGG_CONTINUED_WITHOUT_OPEN_PACKET"
	CodeOpenPacketNotContinued = "OGG_OPEN_PACKET_NOT_CONTINUED"
	CodePacketNotClosed        = "OGG_PACKET_NOT_CLOSED"

	// Codec/header level.
	CodeFirstPacketNotHead  = "OPUS_FIRST_PACKET_NOT_OPUSHEAD"
	CodeSecondPacketNotTags = "OPUS_SECOND_PACKET_NOT_OPUSTAGS"
	CodeOpusTagsMalformed   = "OPUS_OPUSTAGS_MALFORMED"
	CodeOpusHeadMalformed   = "OPUS_OPUSHEAD_MALFORMED"
	CodeUnsupportedMapping  = "OPUS_UNSUPPORTED_CHANNEL_MAPPING"
	CodeAudioOnHeaderPage   = "OPUS_AUDIO_ON_HEADER_PAGE"
	CodeEmptyAudioPacket    = "OPUS_EMPTY_AUDIO_PACKET"
	CodePacketFraming       = "OPUS_PACKET_FRAMING_INVALID"
	CodePacketTooLong       = "OPUS_FRAME_TOO_LONG"
	CodePacketDuration      = "OPUS_PACKET_DURATION_TOO_LONG"

	// Granule timeline level.
	CodeHeaderGranuleNotZero  = "OPUS_HEADER_GRANULE_NOT_ZERO"
	CodeGranuleMustBeMinusOne = "OPUS_GRANULE_MUST_BE_MINUS_ONE"
	CodeGranuleMismatch       = "OPUS_GRANULE_MISMATCH"
	CodeFirstGranuleTooSmall  = "OPUS_FIRST_GRANULE_TOO_SMALL"
	CodeEOSGranuleRange       = "OPUS_EOS_GRANULE_OUT_OF_RANGE"
	CodeEOSWithoutAudio       = "OPUS_EOS_WITHOUT_AUDIO"
)

// Error is a validation failure tied to the zero-based index of the first
// page that proves the stream invalid. Page is -1 when the failure is not
// attributable to a single page (e.g. an empty body).
type Error struct {
	Code    string `json:"code"`
	Page    int    `json:"page"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e.Page < 0 {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s (page %d): %s", e.Code, e.Page, e.Message)
}

// New builds an *Error attributed to the given zero-based page index.
func New(code string, page int, format string, args ...any) *Error {
	return &Error{Code: code, Page: page, Message: fmt.Sprintf(format, args...)}
}
