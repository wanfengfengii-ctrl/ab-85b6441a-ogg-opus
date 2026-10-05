# Ocean Acoustic Station — Ogg Opus Archive Auditor

Before an Ogg Opus recording is archived, this service confirms that
page-oriented transport has not corrupted packet boundaries or the sample
timeline, so player error concealment cannot mask truncation or duration
drift.

It accepts a **single logical Ogg Opus stream** and validates, page by page:

- the Ogg capture pattern, version, page sequence/serial numbers, segment
  table, declared body length and **Ogg CRC** (RFC 3533);
- BOS/EOS placement and the **cross-page continued-packet flags**;
- that the first two complete packets are `OpusHead` then `OpusTags`
  (RFC 7845), including OpusHead/OpusTags structure;
- each audio packet's TOC and RFC 6716 frame framing, deriving its
  **48 kHz sample count**;
- the granule-position timeline: `-1` for pages with no completed packet,
  exact cumulative sample count for ordinary audio pages, and bounded end
  trimming on the EOS page.

A legal record yields stable statistics; any corrupt page, unclosed packet
or granule contradiction returns **HTTP 422** with the **first failing page
index** and a **stable error code**.

## API

### `GET /health`

Used by the container health check.

```json
{"status":"ok"}
```

### `POST /api/opus/audit`

- `Content-Type: audio/ogg` (parameters such as `; codecs=opus` are ignored)
- body: one logical stream, at most **8 MiB** and **2048 pages**

Success (`200`):

```json
{
  "pages": 13,
  "audio_packets": 541,
  "decoded_samples": 519360,
  "playable_samples": 518712
}
```

- `pages` — number of physical Ogg pages.
- `audio_packets` — complete Opus audio packets (after the two headers).
- `decoded_samples` — cumulative 48 kHz samples of all complete audio
  packets, summed from their TOC sequences.
- `playable_samples` — samples on the playable timeline: the EOS granule,
  reflecting any legal end trimming.

Failure (`422`):

```json
{ "error": { "code": "OGG_BAD_CRC", "page": 4, "message": "..." } }
```

`page` is zero-based; it is `-1` when no single page is attributable (e.g.
an empty body).

| Code | Meaning |
| --- | --- |
| `OGG_NO_PAGES` | empty body |
| `OGG_TOO_MANY_PAGES` | more than 2048 pages |
| `OGG_CAPTURE_PATTERN` | missing/mangled `OggS` |
| `OGG_PAGE_TRUNCATED` | header or segment table truncated |
| `OGG_BODY_TRUNCATED` | segment table declares bytes that are absent |
| `OGG_BAD_CRC` | page CRC mismatch |
| `OGG_BAD_VERSION` / `OGG_BAD_HEADER_TYPE` | unsupported version / reserved bits |
| `OGG_SERIAL_MISMATCH` | serial changes mid-stream |
| `OGG_SEQUENCE_GAP` | page sequence not consecutive from 0 |
| `OGG_BOS_NOT_FIRST` / `OGG_BOS_AFTER_START` | BOS flag misuse |
| `OGG_PAGES_AFTER_EOS` / `OGG_EOS_MISSING` | EOS placement errors |
| `OGG_CONTINUED_WITHOUT_OPEN_PACKET` | continued flag with no open packet |
| `OGG_OPEN_PACKET_NOT_CONTINUED` | open packet not continued on the next page |
| `OGG_PACKET_NOT_CLOSED` | packet still open at end-of-stream |
| `OPUS_FIRST_PACKET_NOT_OPUSHEAD` | first complete packet is not OpusHead |
| `OPUS_SECOND_PACKET_NOT_OPUSTAGS` | second complete packet is not OpusTags |
| `OPUS_OPUSHEAD_MALFORMED` / `OPUS_OPUSTAGS_MALFORMED` | bad header structure |
| `OPUS_UNSUPPORTED_CHANNEL_MAPPING` | unsupported OpusHead mapping (families 0/1, single encoded stream) |
| `OPUS_EMPTY_AUDIO_PACKET` | zero-octet audio packet |
| `OPUS_PACKET_FRAMING_INVALID` | RFC 6716 frame framing inconsistent/truncated |
| `OPUS_FRAME_TOO_LONG` / `OPUS_PACKET_DURATION_TOO_LONG` | >1275 B/frame or >120 ms/packet |
| `OPUS_HEADER_GRANULE_NOT_ZERO` | header page granule is not 0 |
| `OPUS_GRANULE_MUST_BE_MINUS_ONE` | page with no completed packet does not use `-1` |
| `OPUS_GRANULE_MISMATCH` | ordinary audio page granule ≠ cumulative samples |
| `OPUS_EOS_GRANULE_OUT_OF_RANGE` | EOS granule before the previous anchor or after the cumulative total |
| `OPUS_EOS_WITHOUT_AUDIO` | stream ends with no complete audio packet |

Other statuses: `405` (wrong method), `413` (> 8 MiB), `415` (wrong media
type).

## Running

```sh
# API only; host port is configurable
docker compose up -d --build api
OPUS_AUDIT_HOST_PORT=9090 docker compose up -d --build api

# One-shot verification: waits for api health, then runs Go tests,
# the production build and the HTTP smoke suite (cross-page packet included)
docker compose up --build verify
```

The host port defaults to `8080`; set `OPUS_AUDIT_HOST_PORT` to change it.

## Local development

```sh
go test ./...
go build ./...
go run ./cmd/server -addr :8080
go run ./cmd/smoke -base http://127.0.0.1:8080
```

The repository has no third-party Go dependencies.
