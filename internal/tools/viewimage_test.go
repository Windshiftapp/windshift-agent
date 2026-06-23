package tools

import "testing"

func TestParseAttachmentID(t *testing.T) {
	// Absolute attachment URLs are validated against this run's Windshift host.
	t.Setenv("WS_API_URL", "https://ws.example.com/api")

	ok := []struct {
		name string
		arg  any
		want int
	}{
		{"json number", float64(7), 7},
		{"numeric string", "9", 9},
		{"padded numeric string", "  42 ", 42},
		{"relative download url", "/rest/api/v1/attachments/42/download", 42},
		{"absolute download url, matching host", "https://ws.example.com/rest/api/v1/attachments/123/download", 123},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAttachmentID(tc.arg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}

	bad := []struct {
		name string
		arg  any
	}{
		{"empty string", ""},
		{"zero", float64(0)},
		{"negative", float64(-3)},
		{"non-integer number", float64(1.9)},
		{"non-numeric, non-url", "mockup.png"},
		{"opaque url without attachments segment", "https://evil.example.com/12345"},
		{"url missing api prefix", "/attachments/42/download"},
		{"url with extra trailing segment", "https://ws.example.com/rest/api/v1/attachments/5/raw"},
		{"attachments segment not a download url", "https://ws.example.com/foo/attachments/5/bar"},
		{"canonical path but non-Windshift host", "https://evil.example.com/rest/api/v1/attachments/5/download"},
		{"nil", nil},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseAttachmentID(tc.arg); err == nil {
				t.Errorf("expected error for %v", tc.arg)
			}
		})
	}
}

func TestSniffImageMime(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
		ok   bool
	}{
		{"png", []byte("\x89PNG\r\n\x1a\nrest"), "image/png", true},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}, "image/jpeg", true},
		{"gif87", []byte("GIF87a...."), "image/gif", true},
		{"gif89", []byte("GIF89a...."), "image/gif", true},
		{"webp", append([]byte("RIFF\x00\x00\x00\x00WEBP"), 0x00), "image/webp", true},
		{"not an image", []byte("%PDF-1.7"), "", false},
		{"too short", []byte{0xFF}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mime, ok := sniffImageMime(tc.data)
			if ok != tc.ok || mime != tc.want {
				t.Errorf("sniffImageMime = (%q,%v), want (%q,%v)", mime, ok, tc.want, tc.ok)
			}
		})
	}
}
