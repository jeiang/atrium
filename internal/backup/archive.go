package backup

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
)

const (
	formatVersion = 1
	headerEntry   = "header.json"
	entriesEntry  = "entries.json"
	macEntry      = "manifest.mac"
	dbEntry       = "atrium.db"
	filesPrefix   = "files/"

	chunkSize    = 64 << 10
	prefixLen    = 7
	maxHeader    = 1 << 20
	maxEntryList = 64 << 20
)

type KeyError string

func (e KeyError) Error() string { return string(e) }

const (
	ErrTampered       KeyError = "backup: the archive is corrupt or was modified"
	ErrNotArchive     KeyError = "backup: the file is not an atrium backup archive"
	ErrNewerFormat    KeyError = "backup: the archive uses a newer archive format than this atrium understands"
	ErrNewerSchema    KeyError = "backup: the archive holds a newer database schema than this atrium knows; run the atrium release that made it"
	ErrArchiveKey     KeyError = "backup: the key does not match the key the archive was made under"
	ErrPlaintextKeyed KeyError = "backup: the archive holds a plaintext database but a database key is configured; clear the " + db.KeySetting + " setting or restore an encrypted archive"
)

type Header struct {
	Format    int      `json:"format"`
	CreatedAt string   `json:"created_at"`
	Schema    int      `json:"schema"`
	Encrypted bool     `json:"encrypted"`
	KeyID     string   `json:"key_id,omitempty"`
	KeyIDs    []string `json:"key_ids,omitempty"`
	Verifier  string   `json:"verifier,omitempty"`
}

type Entry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode,omitempty"`
}

type entryList struct {
	Entries []Entry `json:"entries"`
}

func macOf(key db.Key, header, entries []byte) string {
	if key.IsZero() {
		h := sha256.New()
		h.Write(header)
		h.Write([]byte{0})
		h.Write(entries)
		return hex.EncodeToString(h.Sum(nil))
	}
	m := hmac.New(sha256.New, dbkey.BackupMAC(key))
	m.Write(header)
	m.Write([]byte{0})
	m.Write(entries)
	return hex.EncodeToString(m.Sum(nil))
}

func newAEAD(key db.Key) (cipher.AEAD, error) {
	block, err := aes.NewCipher(dbkey.Seal(key))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealedSize(n int64) int64 {
	chunks := max(1, (n+chunkSize-1)/chunkSize)
	return prefixLen + n + chunks*16
}

func chunkNonce(prefix []byte, counter uint32, last bool) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[prefixLen:], counter)
	if last {
		nonce[11] = 1
	}
	return nonce
}

func seal(w io.Writer, aead cipher.AEAD, name string, r io.Reader, size int64) error {
	prefix := make([]byte, prefixLen)
	if _, err := rand.Read(prefix); err != nil {
		return err
	}
	if _, err := w.Write(prefix); err != nil {
		return err
	}
	buf := make([]byte, chunkSize)
	remaining := size
	var counter uint32
	for {
		take := min(remaining, chunkSize)
		if _, err := io.ReadFull(r, buf[:take]); err != nil {
			return fmt.Errorf("%s changed while it was being backed up: %w", name, err)
		}
		remaining -= take
		last := remaining == 0
		if _, err := w.Write(aead.Seal(nil, chunkNonce(prefix, counter, last), buf[:take], []byte(name))); err != nil {
			return err
		}
		if last {
			return nil
		}
		counter++
	}
}

func unseal(w io.Writer, aead cipher.AEAD, name string, r io.Reader) error {
	br := bufio.NewReaderSize(r, chunkSize+aead.Overhead()+1)
	prefix := make([]byte, prefixLen)
	if _, err := io.ReadFull(br, prefix); err != nil {
		return fmt.Errorf("%w: %s is truncated", ErrTampered, name)
	}
	buf := make([]byte, chunkSize+aead.Overhead())
	var counter uint32
	for {
		n, err := io.ReadFull(br, buf)
		last := false
		switch {
		case errors.Is(err, io.ErrUnexpectedEOF):
			last = true
		case err != nil:
			return fmt.Errorf("%w: %s is truncated", ErrTampered, name)
		default:
			_, perr := br.Peek(1)
			last = errors.Is(perr, io.EOF)
		}
		plain, err := aead.Open(nil, chunkNonce(prefix, counter, last), buf[:n], []byte(name))
		if err != nil {
			return fmt.Errorf("%w: %s does not authenticate", ErrTampered, name)
		}
		if _, err := w.Write(plain); err != nil {
			return err
		}
		if last {
			return nil
		}
		counter++
	}
}

type hashingWriter struct {
	w io.Writer
	h hash.Hash
	n int64
}

func newHashingWriter(w io.Writer) *hashingWriter {
	return &hashingWriter{w: w, h: sha256.New()}
}

func (h *hashingWriter) Write(p []byte) (int, error) {
	n, err := h.w.Write(p)
	h.h.Write(p[:n])
	h.n += int64(n)
	return n, err
}

func (h *hashingWriter) sum() string { return hex.EncodeToString(h.h.Sum(nil)) }

func addBytes(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

type source struct {
	name string
	path string
	size int64
	mode uint32
}

func listFiles(root string) ([]source, error) {
	var out []source
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("backup: %s is not a regular file; files/ holds only regular files", p)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, source{name: filesPrefix + filepath.ToSlash(rel), path: p, size: info.Size(), mode: uint32(info.Mode().Perm())})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return out, err
}

func writeEntry(tw *tar.Writer, name string, size int64, mode uint32, fill func(io.Writer) error) (Entry, error) {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, Typeflag: tar.TypeReg}); err != nil {
		return Entry{}, err
	}
	hw := newHashingWriter(tw)
	if err := fill(hw); err != nil {
		return Entry{}, err
	}
	if hw.n != size {
		return Entry{}, fmt.Errorf("backup: %s wrote %d bytes, expected %d", name, hw.n, size)
	}
	return Entry{Name: name, Size: size, SHA256: hw.sum(), Mode: mode}, nil
}

func writeArchive(out io.Writer, header Header, key db.Key, snapshot string, files []source) error {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(out)
	if err := addBytes(tw, headerEntry, headerJSON); err != nil {
		return err
	}
	var aead cipher.AEAD
	if !key.IsZero() {
		if aead, err = newAEAD(key); err != nil {
			return err
		}
	}
	info, err := os.Stat(snapshot)
	if err != nil {
		return err
	}
	var entries []Entry
	snap, err := os.Open(snapshot)
	if err != nil {
		return err
	}
	e, err := writeEntry(tw, dbEntry, info.Size(), 0, func(w io.Writer) error {
		_, err := io.Copy(w, io.LimitReader(snap, info.Size()))
		return err
	})
	_ = snap.Close()
	if err != nil {
		return err
	}
	entries = append(entries, e)
	for _, f := range files {
		src, err := os.Open(f.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		stored := f.size
		fill := func(w io.Writer) error {
			_, err := io.Copy(w, io.LimitReader(src, f.size))
			return err
		}
		if aead != nil {
			stored = sealedSize(f.size)
			fill = func(w io.Writer) error { return seal(w, aead, f.name, src, f.size) }
		}
		e, err := writeEntry(tw, f.name, stored, f.mode, fill)
		_ = src.Close()
		if err != nil {
			return err
		}
		entries = append(entries, e)
	}
	entriesJSON, err := json.Marshal(entryList{Entries: entries})
	if err != nil {
		return err
	}
	if err := addBytes(tw, entriesEntry, entriesJSON); err != nil {
		return err
	}
	if err := addBytes(tw, macEntry, []byte(macOf(key, headerJSON, entriesJSON))); err != nil {
		return err
	}
	return tw.Close()
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTampered, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: an archive metadata entry is oversized", ErrNotArchive)
	}
	return data, nil
}

func readHeader(tr *tar.Reader) (Header, []byte, error) {
	hdr, err := tr.Next()
	if err != nil || hdr.Name != headerEntry {
		return Header{}, nil, ErrNotArchive
	}
	raw, err := readLimited(tr, maxHeader)
	if err != nil {
		return Header{}, nil, err
	}
	var h Header
	if err := json.Unmarshal(raw, &h); err != nil || h.Format == 0 {
		return Header{}, nil, ErrNotArchive
	}
	if h.Format > formatVersion {
		return Header{}, nil, ErrNewerFormat
	}
	return h, raw, nil
}

func validFilesName(name string) (string, bool) {
	rel, ok := strings.CutPrefix(name, filesPrefix)
	if !ok || rel == "" || !fs.ValidPath(rel) || path.Clean(rel) != rel {
		return "", false
	}
	return rel, true
}

type opened struct {
	header  Header
	file    *os.File
	tr      *tar.Reader
	rawHead []byte
}

func openArchive(archive string) (*opened, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(f)
	h, raw, err := readHeader(tr)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &opened{header: h, file: f, tr: tr, rawHead: raw}, nil
}

func (o *opened) Close() error { return o.file.Close() }

func checkKey(h Header, key db.Key) error {
	if h.Encrypted != !key.IsZero() {
		return ErrArchiveKey
	}
	if h.Encrypted && (dbkey.ID(key) != h.KeyID || dbkey.BackupVerifier(key) != h.Verifier) {
		return ErrArchiveKey
	}
	return nil
}

func (o *opened) unpack(ctx context.Context, key db.Key, stage string) (map[string]Entry, error) {
	var aead cipher.AEAD
	if !key.IsZero() {
		var err error
		if aead, err = newAEAD(key); err != nil {
			return nil, err
		}
	}
	seen := map[string]Entry{}
	var entriesJSON []byte
	var mac string
	var staged []string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := o.tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTampered, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("%w: %s is not a regular entry", ErrTampered, hdr.Name)
		}
		switch hdr.Name {
		case entriesEntry:
			if entriesJSON, err = readLimited(o.tr, maxEntryList); err != nil {
				return nil, err
			}
			continue
		case macEntry:
			raw, err := readLimited(o.tr, maxHeader)
			if err != nil {
				return nil, err
			}
			mac = string(raw)
			continue
		}
		if _, dup := seen[hdr.Name]; dup {
			return nil, fmt.Errorf("%w: %s appears twice", ErrTampered, hdr.Name)
		}
		var dest string
		sealed := false
		if hdr.Name == dbEntry {
			dest = filepath.Join(stage, dbEntry)
		} else if rel, ok := validFilesName(hdr.Name); ok {
			dest = filepath.Join(stage, "files", filepath.FromSlash(rel))
			sealed = aead != nil
		} else {
			return nil, fmt.Errorf("%w: unexpected entry %q", ErrTampered, hdr.Name)
		}
		hash := sha256.New()
		body := io.TeeReader(o.tr, hash)
		sink := io.Discard
		var out *os.File
		if stage != "" {
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return nil, err
			}
			if out, err = os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
				return nil, err
			}
			sink = out
			staged = append(staged, dest)
		}
		if sealed {
			err = unseal(sink, aead, hdr.Name, body)
		} else {
			_, err = io.Copy(sink, body)
		}
		if out != nil {
			err = errors.Join(err, out.Close())
		}
		if err != nil {
			return nil, err
		}
		seen[hdr.Name] = Entry{Name: hdr.Name, Size: hdr.Size, SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	if entriesJSON == nil || mac == "" {
		return nil, fmt.Errorf("%w: the archive is incomplete", ErrTampered)
	}
	if !hmac.Equal([]byte(mac), []byte(macOf(key, o.rawHead, entriesJSON))) {
		return nil, ErrTampered
	}
	var list entryList
	if err := json.Unmarshal(entriesJSON, &list); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTampered, err)
	}
	if len(list.Entries) != len(seen) {
		return nil, fmt.Errorf("%w: the entry list does not match the archive", ErrTampered)
	}
	if _, ok := seen[dbEntry]; !ok {
		return nil, fmt.Errorf("%w: the archive has no database", ErrTampered)
	}
	listed := map[string]Entry{}
	for _, want := range list.Entries {
		got, ok := seen[want.Name]
		if !ok || got.Size != want.Size || got.SHA256 != want.SHA256 {
			return nil, fmt.Errorf("%w: %s does not match the entry list", ErrTampered, want.Name)
		}
		listed[want.Name] = want
	}
	if stage != "" {
		for _, p := range staged {
			name := filepath.ToSlash(strings.TrimPrefix(p, stage+string(filepath.Separator)))
			if mode := listed[name].Mode; mode != 0 {
				if err := os.Chmod(p, fs.FileMode(mode)&0o777|0o600); err != nil {
					return nil, err
				}
			}
		}
	}
	return listed, nil
}
