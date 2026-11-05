package recon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/core"
)

// maxFileBytes caps a receipt upload, enforced on the whole request body
// via http.MaxBytesReader in the route handler (so it gives 413, not a
// SaveFile-level 422).
const maxFileBytes = 10 << 20 // 10 MiB

// allowedFileMIME lists the MIME types SaveFile accepts, sniffed from the
// file's own bytes rather than trusted from the upload's filename or
// declared Content-Type.
var allowedFileMIME = map[string]bool{
	"application/pdf": true,
	"image/jpeg":      true,
	"image/png":       true,
}

// File is an uploaded receipt. Its bytes live on disk at a path derived
// only from OrgID and the hex SHA256 (see StoredPath) - never from
// OriginalName, which is kept purely as download metadata.
type File struct {
	ID           int64
	OrgID        int64
	SHA256       []byte
	MIME         string
	SizeBytes    int
	OriginalName string
	CreatedAt    time.Time
}

// StoredPath returns the path under filesDir where f's bytes are written.
func (f File) StoredPath(filesDir string) string {
	return filepath.Join(filesDir, strconv.FormatInt(f.OrgID, 10), hex.EncodeToString(f.SHA256))
}

const fileColumns = `id, org_id, sha256, mime, size_bytes, original_name, created_at`

func scanFile(row pgx.Row) (File, error) {
	var f File
	err := row.Scan(&f.ID, &f.OrgID, &f.SHA256, &f.MIME, &f.SizeBytes, &f.OriginalName, &f.CreatedAt)
	return f, err
}

// SaveFile reads r fully, sniffs its MIME type from the first 512 bytes
// (http.DetectContentType), and rejects anything other than PDF/JPEG/PNG
// with ErrInvalidInput (422). It writes the bytes atomically (a temp file
// in the same directory, renamed into place) to
// <filesDir>/<org id>/<hex sha256> before inserting the row, so a reader
// never observes a partially written file. If an (org_id, sha256) row
// already exists - including a concurrent upload of the same content - the
// existing row is returned and the existing file is kept.
//
// r's size is bounded by the caller via http.MaxBytesReader on the
// request body; a read that trips that limit surfaces here as an
// *http.MaxBytesError, which callers should map to 413 rather than 422.
func SaveFile(ctx context.Context, q core.Querier, orgID int64, filesDir, name string, r io.Reader) (File, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return File{}, err
	}

	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	mime := http.DetectContentType(head)
	if !allowedFileMIME[mime] {
		return File{}, fmt.Errorf("%w: unsupported file type %q", core.ErrInvalidInput, mime)
	}

	sum := sha256.Sum256(data)
	dir := filepath.Join(filesDir, strconv.FormatInt(orgID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return File{}, err
	}
	dest := filepath.Join(dir, hex.EncodeToString(sum[:]))
	if err := writeFileAtomic(dest, data); err != nil {
		return File{}, err
	}

	if name == "" {
		name = "receipt"
	}
	// ON CONFLICT DO NOTHING (rather than catching the unique_violation
	// after the fact) avoids aborting the caller's transaction: a plain
	// insert that hits the constraint would leave the transaction unable
	// to run the follow-up GetFileByHash lookup below.
	row := q.QueryRow(ctx, `
		INSERT INTO files (org_id, sha256, mime, size_bytes, original_name)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id, sha256) DO NOTHING
		RETURNING `+fileColumns,
		orgID, sum[:], mime, len(data), name,
	)
	created, err := scanFile(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return GetFileByHash(ctx, q, orgID, sum[:])
	}
	return created, err
}

// writeFileAtomic writes data to dest by creating a temp file in dest's
// directory and renaming it into place, so a concurrent reader of dest
// never sees a partial write.
func writeFileAtomic(dest string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".upload-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

// GetFileByHash looks up a file by its content hash, scoped to orgID. It
// returns core.ErrNotFound if no such file exists in that org.
func GetFileByHash(ctx context.Context, q core.Querier, orgID int64, sha256 []byte) (File, error) {
	row := q.QueryRow(ctx, `SELECT `+fileColumns+` FROM files WHERE org_id = $1 AND sha256 = $2`, orgID, sha256)
	f, err := scanFile(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, core.ErrNotFound
	}
	return f, err
}

// GetFile looks up a file by id, scoped to orgID. It returns
// core.ErrNotFound if no such file exists in that org - including when the
// id belongs to another org.
func GetFile(ctx context.Context, q core.Querier, orgID, id int64) (File, error) {
	row := q.QueryRow(ctx, `SELECT `+fileColumns+` FROM files WHERE org_id = $1 AND id = $2`, orgID, id)
	f, err := scanFile(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, core.ErrNotFound
	}
	return f, err
}
