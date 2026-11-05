package recon

import "testing"

// TestSanitizeFilename covers the characters sanitizeFilename must strip
// before a name is safe to put inside a Content-Disposition header: quotes
// and path separators (which could otherwise be read as a path), CR/LF
// (header injection), and other Unicode control runes more generally.
func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "receipt.pdf", "receipt.pdf"},
		{"quote and CRLF header injection", "a\"\r\nX: y.png", "aX: y.png"},
		{"path separators", `../../etc/passwd`, "....etcpasswd"},
		{"backslash", `C:\Windows\x.png`, "C:Windowsx.png"},
		{"control runes", "a\x00b\x7fc\td", "abcd"},
		{"empty after stripping", "\"\r\n", "receipt"},
		{"empty input", "", "receipt"},
		{"whitespace only", "   ", "receipt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeFilename(tc.in); got != tc.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
