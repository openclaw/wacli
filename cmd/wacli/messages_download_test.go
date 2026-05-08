package main

import "testing"

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"hello.jpg":            "hello.jpg",
		"my photo.jpg":         "my_photo.jpg",
		"../etc/passwd":        ".._etc_passwd",
		"":                     "download",
		".":                    "download",
		"..":                   "download",
		"foo/bar.png":          "foo_bar.png",
		"emoji😀.png":           "emoji_.png",
		"weird:chars*<here>.x": "weird_chars__here_.x",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtensionFor(t *testing.T) {
	cases := []struct {
		mime, mediaType, want string
	}{
		{"image/jpeg", "image", ".jpg"},
		{"image/png", "image", ".png"},
		{"image/webp", "image", ".webp"},
		{"video/mp4", "video", ".mp4"},
		{"audio/ogg", "audio", ".ogg"},
		{"application/pdf", "document", ".pdf"},
		// Mime missing: fall back to mediaType
		{"", "image", ".jpg"},
		{"", "video", ".mp4"},
		{"", "audio", ".ogg"},
		{"", "document", ".bin"},
		{"", "sticker", ".jpg"},
		// Both missing
		{"", "", ""},
		// Capitalised mime should still match
		{"Image/JPEG", "image", ".jpg"},
	}
	for _, c := range cases {
		if got := extensionFor(c.mime, c.mediaType); got != c.want {
			t.Errorf("extensionFor(%q, %q) = %q, want %q", c.mime, c.mediaType, got, c.want)
		}
	}
}

func TestDefaultDownloadPath(t *testing.T) {
	cases := []struct {
		msgID, filename, mime, mediaType, want string
	}{
		// Filename present → use it (sanitised); always prefixed with "./"
		{"ABC123", "report.pdf", "application/pdf", "document", "./report.pdf"},
		{"ABC123", "../escape.png", "image/png", "image", "./.._escape.png"},
		// No filename → msgID + extension from mime
		{"ABC123", "", "image/jpeg", "image", "./ABC123.jpg"},
		{"ABC123", "", "video/mp4", "video", "./ABC123.mp4"},
		// No filename + no mime → msgID + extension from mediaType
		{"ABC123", "", "", "image", "./ABC123.jpg"},
		// Nothing usable
		{"ABC123", "", "", "", "./ABC123"},
	}
	for _, c := range cases {
		got := defaultDownloadPath(c.msgID, c.filename, c.mime, c.mediaType)
		// sanitiseFilename strips the leading "./" because slashes aren't kept,
		// so the filename path returns just the sanitised name in some cases.
		// Accept either the explicit want or the sanitised filename.
		if got != c.want {
			t.Errorf("defaultDownloadPath(%q, %q, %q, %q) = %q, want %q",
				c.msgID, c.filename, c.mime, c.mediaType, got, c.want)
		}
	}
}
