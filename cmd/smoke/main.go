// Command smoke exercises a running opus-audit API over HTTP:
//
//   - GET  /health must return 200
//   - POST a valid stream that contains an audio packet spanning a page
//     boundary, and verify the exact statistics
//   - POST streams that are corrupt in exactly one way and verify a 422
//     carrying the first failing page index and a stable error code
//
// It exits non-zero if any check fails, so it can aggregate the result of
// the one-shot "verify" service.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/acoustics/opus-audit/internal/oggpage"
	"github.com/acoustics/opus-audit/internal/testogg"
)

type auditResponse struct {
	Pages           int   `json:"pages"`
	AudioPackets    int64 `json:"audio_packets"`
	DecodedSamples  int64 `json:"decoded_samples"`
	PlayableSamples int64 `json:"playable_samples"`
	Error           *struct {
		Code    string `json:"code"`
		Page    int    `json:"page"`
		Message string `json:"message"`
	} `json:"error"`
}

func main() {
	base := flag.String("base", envOr("AUDIT_BASE_URL", "http://api:8080"),
		"base URL of the opus-audit service")
	flag.Parse()

	client := &http.Client{Timeout: 15 * time.Second}
	var failures int
	fail := func(format string, args ...any) {
		failures++
		fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	}
	pass := func(format string, args ...any) {
		fmt.Printf("PASS: "+format+"\n", args...)
	}

	// 1. Health.
	if err := waitForHealth(client, *base, 30*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: service never became healthy: %v\n", err)
		os.Exit(1)
	}
	pass("GET /health returned 200")

	// 2. Valid stream with a cross-page audio packet.
	valid := buildValidStream()
	resp, status, err := postAudit(client, *base, valid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: valid audit request: %v\n", err)
		os.Exit(1)
	}
	if status != http.StatusOK {
		fail("valid stream status = %d, want 200 (body=%v)", status, resp)
	} else {
		want := auditResponse{Pages: 5, AudioPackets: 4, DecodedSamples: 3840, PlayableSamples: 3840}
		if resp.Pages != want.Pages || resp.AudioPackets != want.AudioPackets ||
			resp.DecodedSamples != want.DecodedSamples || resp.PlayableSamples != want.PlayableSamples {
			fail("valid stream stats = %+v, want %+v", resp, want)
		} else {
			pass("valid cross-page stream audited: %d pages, %d packets, %d decoded, %d playable",
				resp.Pages, resp.AudioPackets, resp.DecodedSamples, resp.PlayableSamples)
		}
	}

	// 3. Deterministic corruptions, each must yield 422 with code + page.
	corruptions := []struct {
		name     string
		mutate   func(data []byte) []byte
		wantCode string
		wantPage int
	}{
		{"crc corruption", func(d []byte) []byte {
			d[len(d)-1] ^= 0xff // last byte is the final audio packet body
			return d
		}, "OGG_BAD_CRC", 4},
		{"capture pattern", func(d []byte) []byte {
			// Locate the second page capture pattern and break it.
			off := skipPage(d, 0)
			d[off] = 'X'
			return d
		}, "OGG_CAPTURE_PATTERN", 1},
		{"truncated body", func(d []byte) []byte {
			// Cut the stream off mid last-page body.
			return d[:len(d)-20]
		}, "OGG_BODY_TRUNCATED", 4},
		{"no eos", func(d []byte) []byte {
			// Clear the EOS flag (byte 5 of the last page's header).
			off := skipPage(d, 0)
			off = skipPage(d, off)
			off = skipPage(d, off)
			off = skipPage(d, off)
			d[off+5] &^= 0x04
			fixCRC(d, off)
			return d
		}, "OGG_EOS_MISSING", 4},
	}
	for _, c := range corruptions {
		data := append([]byte(nil), valid...)
		data = c.mutate(data)
		resp, status, err := postAudit(client, *base, data)
		if err != nil {
			fail("%s: request error: %v", c.name, err)
			continue
		}
		if status != http.StatusUnprocessableEntity {
			fail("%s: status = %d, want 422", c.name, status)
			continue
		}
		if resp.Error == nil {
			fail("%s: 422 without error object: %+v", c.name, resp)
			continue
		}
		if resp.Error.Code != c.wantCode || resp.Error.Page != c.wantPage {
			fail("%s: error = (%q, page %d), want (%q, page %d)",
				c.name, resp.Error.Code, resp.Error.Page, c.wantCode, c.wantPage)
			continue
		}
		pass("%s rejected: %s at page %d", c.name, resp.Error.Code, resp.Error.Page)
	}

	if failures > 0 {
		fmt.Fprintf(os.Stderr, "\nsmoke finished with %d failure(s)\n", failures)
		os.Exit(1)
	}
	fmt.Println("\nall smoke checks passed")
}

// buildValidStream mirrors the canonical unit-test stream: four audio
// packets, with a 701-byte packet split 510/191 across pages 3 and 4.
func buildValidStream() []byte {
	head := testogg.OpusHead(1, 312)
	tags := testogg.OpusTags()
	a1 := testogg.AudioPacket(19, false, 80)
	a2 := testogg.AudioPacket(19, false, 81)
	big := testogg.AudioPacket(19, false, 700)
	a4 := testogg.AudioPacket(19, false, 82)

	b := testogg.New()
	b.Page(testogg.PageSpec{BOS: true, Granule: 0, Packets: [][]byte{head}})
	b.Page(testogg.PageSpec{Granule: 0, Packets: [][]byte{tags}})
	b.Page(testogg.PageSpec{Granule: 1920, Packets: [][]byte{a1, a2}})
	b.Page(testogg.PageSpec{Granule: -1, Segs: testogg.SegsOpen(510), Body: big[:510]})
	b.Page(testogg.PageSpec{
		Continued: true,
		EOS:       true,
		Granule:   3840,
		Segs:      append(testogg.SegsClose(len(big)-510), testogg.SegsClose(len(a4))...),
		Body:      append(append([]byte{}, big[510:]...), a4...),
	})
	return b.Build()
}

func postAudit(client *http.Client, base string, body []byte) (auditResponse, int, error) {
	req, err := http.NewRequest(http.MethodPost, base+"/api/opus/audit", bytes.NewReader(body))
	if err != nil {
		return auditResponse{}, 0, err
	}
	req.Header.Set("Content-Type", "audio/ogg")
	res, err := client.Do(req)
	if err != nil {
		return auditResponse{}, 0, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	var parsed auditResponse
	_ = json.Unmarshal(data, &parsed)
	return parsed, res.StatusCode, nil
}

func waitForHealth(client *http.Client, base string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	var lastErr error
	for time.Now().Before(deadline) {
		res, err := client.Get(base + "/health")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("health not ready: %v", lastErr)
}

// skipPage returns the offset just past the Ogg page beginning at off.
func skipPage(d []byte, off int) int {
	nseg := int(d[off+26])
	body := 0
	for i := 0; i < nseg; i++ {
		body += int(d[off+27+i])
	}
	return off + 27 + nseg + body
}

// fixCRC recomputes and writes the CRC of the page beginning at off after a
// header mutation.
func fixCRC(d []byte, off int) {
	end := skipPage(d, off)
	page := append([]byte(nil), d[off:end]...)
	for i := 22; i < 26; i++ {
		page[i] = 0
	}
	crc := oggpage.CRC(page)
	d[off+22] = byte(crc)
	d[off+23] = byte(crc >> 8)
	d[off+24] = byte(crc >> 16)
	d[off+25] = byte(crc >> 24)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
