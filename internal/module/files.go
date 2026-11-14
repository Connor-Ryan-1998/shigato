package module

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/auth"
	"shigoto/internal/core"
)

// SanitizeFilename strips characters that would break or hijack the
// Content-Disposition header (quotes, CR/LF header injection, and path
// separators, in case a client ever reads this value as a path), plus any
// other Unicode control rune (NUL, TAB, DEL/0x7f, etc.) that could confuse
// a header parser or terminal even without matching one of those specific
// cases. Falls back to "receipt" if nothing usable remains.
func SanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '"' || r == '/' || r == '\\':
			continue
		case unicode.IsControl(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return "receipt"
	}
	return s
}

// FileResponse is the JSON shape of an uploaded file.
type FileResponse struct {
	ID           int64  `json:"id"`
	MIME         string `json:"mime"`
	SizeBytes    int    `json:"size_bytes"`
	OriginalName string `json:"original_name"`
}

// FileToResponse converts f to its JSON shape.
func FileToResponse(f core.File) FileResponse {
	return FileResponse{ID: f.ID, MIME: f.MIME, SizeBytes: f.SizeBytes, OriginalName: f.OriginalName}
}

// HandleUpload handles a multipart "file" upload. The request body is
// capped at core.MaxFileBytes via http.MaxBytesReader; exceeding it gives
// 413, checked both at ParseMultipartForm (a declared-too-large body) and
// around SaveFile (a streamed body that only trips the limit while being
// read), rather than folding either into the generic 422/500 paths.
// SaveFile, the audit row and onSaved (when non-nil) run in one
// transaction. On failure HandleUpload writes the error response itself
// and returns false; on success it returns the file without writing a
// response, so the caller chooses the status and body.
func HandleUpload(w http.ResponseWriter, r *http.Request, d Deps, onSaved func(tx pgx.Tx, f core.File) error) (core.File, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, core.MaxFileBytes)
	if err := r.ParseMultipartForm(core.MaxFileBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			WriteError(w, http.StatusRequestEntityTooLarge, "file too large")
			return core.File{}, false
		}
		WriteError(w, http.StatusBadRequest, "invalid multipart body")
		return core.File{}, false
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "file is required")
		return core.File{}, false
	}
	defer file.Close()

	p := auth.From(r.Context())
	var created core.File
	err = RunInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		created, err = core.SaveFile(r.Context(), tx, p.OrgID, d.Cfg.FilesDir, header.Filename, file)
		if err != nil {
			return err
		}
		if err := core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "file", created.ID, "create", map[string]any{
			"mime": created.MIME, "size_bytes": created.SizeBytes,
		}); err != nil {
			return err
		}
		if onSaved != nil {
			return onSaved(tx, created)
		}
		return nil
	})
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			WriteError(w, http.StatusRequestEntityTooLarge, "file too large")
			return core.File{}, false
		}
		WriteCoreError(w, err)
		return core.File{}, false
	}
	return created, true
}

// ServeFile streams f's bytes with its stored (sniffed) MIME type.
// X-Content-Type-Options: nosniff stops a browser from second-guessing
// that type from the content; Content-Disposition forces a download using
// the sanitized original name rather than the server path.
func ServeFile(w http.ResponseWriter, f core.File, filesDir string) {
	stored, err := os.Open(f.StoredPath(filesDir))
	if err != nil {
		WriteCoreError(w, fmt.Errorf("opening stored file: %w", err))
		return
	}
	defer stored.Close()

	w.Header().Set("Content-Type", f.MIME)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="`+SanitizeFilename(f.OriginalName)+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, stored)
}
