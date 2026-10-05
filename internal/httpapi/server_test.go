package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/acoustics/opus-audit/internal/testogg"
)

func testServer() http.Handler {
	return NewServer(log.New(io.Discard, "", 0))
}

func validStream() []byte {
	head := testogg.OpusHead(2, 312)
	tags := testogg.OpusTags()
	a1 := testogg.AudioPacket(19, true, 60)
	big := testogg.AudioPacket(19, true, 600) // 601 bytes -> spans a page
	a3 := testogg.AudioPacket(19, true, 61)
	b := testogg.New()
	b.Page(testogg.PageSpec{BOS: true, Granule: 0, Packets: [][]byte{head}})
	b.Page(testogg.PageSpec{Granule: 0, Packets: [][]byte{tags}})
	b.Page(testogg.PageSpec{Granule: 960, Packets: [][]byte{a1}})
	b.Page(testogg.PageSpec{Granule: -1, Segs: testogg.SegsOpen(510), Body: big[:510]})
	b.Page(testogg.PageSpec{
		Continued: true,
		EOS:       true,
		Granule:   2880,
		Segs:      append(testogg.SegsClose(len(big)-510), testogg.SegsClose(len(a3))...),
		Body:      append(append([]byte{}, big[510:]...), a3...),
	})
	return b.Build()
}

func TestHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	testServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestAuditOK(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/opus/audit", bytes.NewReader(validStream()))
	req.Header.Set("Content-Type", "audio/ogg")
	rec := httptest.NewRecorder()
	testServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var stats struct {
		Pages           int   `json:"pages"`
		AudioPackets    int64 `json:"audio_packets"`
		DecodedSamples  int64 `json:"decoded_samples"`
		PlayableSamples int64 `json:"playable_samples"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if stats != struct {
		Pages           int   `json:"pages"`
		AudioPackets    int64 `json:"audio_packets"`
		DecodedSamples  int64 `json:"decoded_samples"`
		PlayableSamples int64 `json:"playable_samples"`
	}{5, 3, 2880, 2880} {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestAuditRejectsCorruption(t *testing.T) {
	data := validStream()
	// Corrupt a body byte on the first audio page without updating CRC.
	data[len(data)-1] ^= 0xff
	req := httptest.NewRequest(http.MethodPost, "/api/opus/audit", bytes.NewReader(data))
	req.Header.Set("Content-Type", "audio/ogg")
	rec := httptest.NewRecorder()
	testServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error struct {
			Code string `json:"code"`
			Page int    `json:"page"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error.Code != "OGG_BAD_CRC" {
		t.Fatalf("code = %q, want OGG_BAD_CRC", resp.Error.Code)
	}
}

func TestAuditContentTypeRequired(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/opus/audit", bytes.NewReader(validStream()))
	rec := httptest.NewRecorder()
	testServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
}

func TestAuditMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/opus/audit", nil)
	rec := httptest.NewRecorder()
	testServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestAuditTooLarge(t *testing.T) {
	big := bytes.Repeat([]byte{'O'}, MaxBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/opus/audit", bytes.NewReader(big))
	req.Header.Set("Content-Type", "audio/ogg")
	rec := httptest.NewRecorder()
	testServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}
