// Package files persists rendered PNGs to disk under a content-addressed
// filename and looks them up later. It also dedupes concurrent renders of
// the same hash via singleflight so 20 simultaneous "Buy Shirt" clicks for
// the same design produce one render and one file write, not twenty.
package files

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"golang.org/x/sync/singleflight"
)

// hashPattern matches a 64-character lowercase hex string. Any path component
// that doesn't match this regex is rejected before touching the filesystem,
// which forecloses path-traversal (../../etc/passwd) and case-folding
// shenanigans on case-insensitive filesystems.
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ErrInvalidHash is returned by Get/Path/Save when the hash isn't 64 hex chars.
var ErrInvalidHash = errors.New("invalid file hash")

// ErrNotFound is returned by Get when no PNG exists for the given hash.
var ErrNotFound = errors.New("file not found")

// Store owns the data directory and the singleflight group used to dedupe
// concurrent renders. Construct one with New and reuse it for the life of
// the server.
type Store struct {
	dir string
	sf  singleflight.Group
}

// New constructs a Store rooted at dir. The directory is created if it
// doesn't exist (with 0o755). Caller is responsible for choosing a
// persistent location — by convention the server uses ./data/files but the
// DATA_DIR env var can override.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("files: empty data dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("files: mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the configured data directory. Useful for logging.
func (s *Store) Dir() string { return s.dir }

// Path returns the absolute path for a given hash, or ErrInvalidHash if the
// hash doesn't match the expected pattern. Does not check that the file
// exists — for that use Exists.
func (s *Store) Path(hash string) (string, error) {
	if !hashPattern.MatchString(hash) {
		return "", ErrInvalidHash
	}
	return filepath.Join(s.dir, hash+".png"), nil
}

// Exists reports whether a PNG for `hash` is present on disk.
func (s *Store) Exists(hash string) (bool, error) {
	path, err := s.Path(hash)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Get returns the bytes of the PNG for `hash`. Returns ErrNotFound if no
// such file exists. Used by the GET /api/files/{hash}.png handler.
func (s *Store) Get(hash string) ([]byte, error) {
	path, err := s.Path(hash)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// Open returns an io.ReadCloser for the PNG so handlers can stream the file
// to the client without reading it all into memory. Caller must Close.
func (s *Store) Open(hash string) (io.ReadCloser, int64, error) {
	path, err := s.Path(hash)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, stat.Size(), nil
}

// Save writes png to disk under hash, atomically. Writes to {hash}.png.tmp
// first then renames into place so a concurrent reader never sees a partial
// file. No-ops if the file already exists (the hash is content-addressed,
// so a same-hash file already on disk has the same content by definition).
func (s *Store) Save(hash string, png []byte) error {
	path, err := s.Path(hash)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil // already saved by a prior request
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, png, 0o644); err != nil {
		return fmt.Errorf("files: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("files: rename: %w", err)
	}
	return nil
}

// SaveDedup runs `produce` exactly once per hash even if many goroutines call
// it concurrently. Returns the saved bytes (the result of the first producer)
// so the caller can return the PNG to the client without a second disk read.
//
// If the file already exists on disk (from a previous run), `produce` is not
// called and the bytes are read from disk.
func (s *Store) SaveDedup(hash string, produce func() ([]byte, error)) ([]byte, error) {
	if !hashPattern.MatchString(hash) {
		return nil, ErrInvalidHash
	}
	v, err, _ := s.sf.Do(hash, func() (any, error) {
		// Re-check inside the singleflight critical section: another
		// goroutine that arrived just before us may have already saved.
		if existing, err := s.Get(hash); err == nil {
			return existing, nil
		}
		png, err := produce()
		if err != nil {
			return nil, err
		}
		if err := s.Save(hash, png); err != nil {
			return nil, err
		}
		return png, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}
