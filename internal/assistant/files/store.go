package files

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"

	"github.com/jeiang/atrium/internal/db"
)

type Row struct {
	ID        int64   `json:"id"`
	ThreadID  *int64  `json:"thread_id"`
	RunID     *int64  `json:"run_id"`
	Name      string  `json:"name"`
	MediaType string  `json:"media_type"`
	Bytes     int64   `json:"bytes"`
	Path      string  `json:"path"`
	Origin    string  `json:"origin"`
	CreatedAt float64 `json:"created_at"`
	Text      *string `json:"text"`
	Tokens    int64   `json:"tokens"`
	ProjectID *int64  `json:"project_id"`
}

const columns = "id, thread_id, run_id, name, media_type, bytes, path, origin, created_at, text, tokens, project_id"

func scan(r interface{ Scan(...any) error }) (Row, error) {
	var f Row
	err := r.Scan(&f.ID, &f.ThreadID, &f.RunID, &f.Name, &f.MediaType, &f.Bytes, &f.Path, &f.Origin, &f.CreatedAt,
		&f.Text, &f.Tokens, &f.ProjectID)
	return f, err
}

type ownerKind uint8

const (
	threadOwner ownerKind = iota + 1
	projectOwner
)

type Owner struct {
	kind ownerKind
	id   int64
}

func ThreadOwner(id int64) Owner  { return Owner{kind: threadOwner, id: id} }
func ProjectOwner(id int64) Owner { return Owner{kind: projectOwner, id: id} }

// Dir returns the owner's directory name under the files root.
func (o Owner) Dir() string { return o.dirName() }

func (o Owner) dirName() string {
	if o.kind == projectOwner {
		return "p" + strconv.FormatInt(o.id, 10)
	}
	return strconv.FormatInt(o.id, 10)
}

func (o Owner) where() (string, int64) {
	if o.kind == projectOwner {
		return "project_id", o.id
	}
	return "thread_id", o.id
}

func (o Owner) columns() (any, any) {
	switch o.kind {
	case threadOwner:
		return o.id, nil
	case projectOwner:
		return nil, o.id
	}
	return nil, nil
}

func OwnerOf(r *Row) (Owner, bool) {
	switch {
	case r.ThreadID != nil:
		return ThreadOwner(*r.ThreadID), true
	case r.ProjectID != nil:
		return ProjectOwner(*r.ProjectID), true
	}
	return Owner{}, false
}

func nullable(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func Get(ctx context.Context, q db.Querier, fileID int64) (*Row, error) {
	f, err := scan(q.QueryRowContext(ctx, "SELECT "+columns+" FROM thread_files WHERE id = ?", fileID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func OfThread(ctx context.Context, q db.Querier, threadID int64) ([]Row, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM thread_files WHERE thread_id = ? ORDER BY id", threadID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Row, error) { return scan(r) })
}

func OfProject(ctx context.Context, q db.Querier, projectID int64) ([]Row, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM thread_files WHERE project_id = ? ORDER BY id", projectID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Row, error) { return scan(r) })
}

type newFile struct {
	Name      string
	Data      []byte
	MediaType string
	Origin    string
	Owner     Owner
	RunID     int64
	Text      *string
	Tokens    int64
}

func writeStored(dir string, owner Owner, fileID int64, name string, data []byte, chars int) (string, error) {
	path := filepath.Join(dir, owner.dirName(), strconv.FormatInt(fileID, 10)+"-"+safeName(name, chars))
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o666)
}

func store(ctx context.Context, q db.Querier, dir string, f newFile, now float64) (int64, error) {
	chars, err := nameChars(ctx, q)
	if err != nil {
		return 0, err
	}
	threadID, projectID := f.Owner.columns()
	res, err := q.ExecContext(ctx,
		"INSERT INTO thread_files (thread_id, project_id, run_id, name, media_type, bytes, path, origin, created_at, text, tokens) "+
			"VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?)",
		threadID, projectID, nullable(f.RunID), f.Name, f.MediaType, len(f.Data), f.Origin, now, f.Text, f.Tokens)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	path, err := writeStored(dir, f.Owner, id, f.Name, f.Data, chars)
	if err != nil {
		return 0, err
	}
	_, err = q.ExecContext(ctx, "UPDATE thread_files SET path = ? WHERE id = ?", path, id)
	return id, err
}

type UploadedFile struct {
	Name string
	Data []byte
	Type string
}

type AttachmentBlock struct {
	Type      string `json:"type"`
	FileID    int64  `json:"file_id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Kind      string `json:"kind,omitempty"`
	Tokens    int64  `json:"tokens"`
}

func newAttachment(id int64, name, mediaType, kind string, tokens int64) AttachmentBlock {
	return AttachmentBlock{Type: Attachment, FileID: id, Name: name, MediaType: mediaType, Kind: kind, Tokens: tokens}
}

func IngestUploads(ctx context.Context, d *db.DB, dir string, uploads []UploadedFile, now float64, owner Owner, contextLimit *int64) ([]AttachmentBlock, error) {
	if len(uploads) == 0 {
		return nil, nil
	}
	limits, err := ReadLimits(ctx, d.Read)
	if err != nil {
		return nil, err
	}
	total := 0
	for _, u := range uploads {
		total += len(u.Data)
	}
	if total > limits.MessageCap {
		return nil, MessageCapError(limits.MessageCap)
	}
	type inspected struct {
		name string
		data []byte
		info Info
	}
	found := make([]inspected, 0, len(uploads))
	for _, u := range uploads {
		info, err := inspect(u.Name, u.Data, u.Type, limits)
		if err != nil {
			return nil, err
		}
		name := path.Base(u.Name)
		if name == "" {
			name = "file"
		}
		found = append(found, inspected{name, u.Data, info})
	}
	if contextLimit != nil {
		items := make([]nameTokens, len(found))
		for i, f := range found {
			items[i] = nameTokens{f.name, f.info.Tokens}
		}
		if err := checkBudget(items, *contextLimit, limits); err != nil {
			return nil, err
		}
	}
	blocks := make([]AttachmentBlock, 0, len(found))
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, f := range found {
			id, err := store(ctx, tx, dir, newFile{
				Name: f.name, Data: f.data, MediaType: f.info.MediaType, Origin: Upload, Owner: owner, Text: f.info.Text, Tokens: f.info.Tokens,
			}, now)
			if err != nil {
				return err
			}
			blocks = append(blocks, newAttachment(id, f.name, f.info.MediaType, f.info.Kind, f.info.Tokens))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return blocks, nil
}

func clone(ctx context.Context, q db.Querier, from Scope, fileID, threadID int64, now float64) (int64, bool, error) {
	chars, err := nameChars(ctx, q)
	if err != nil {
		return 0, false, err
	}
	found, err := Get(ctx, q, fileID)
	if err != nil || found == nil || !from.owns(found) {
		return 0, false, err
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO thread_files (thread_id, run_id, name, media_type, bytes, path, origin, created_at, text, tokens) VALUES (?, NULL, ?, ?, ?, '', ?, ?, ?, ?)",
		threadID, found.Name, found.MediaType, found.Bytes, found.Origin, now, found.Text, found.Tokens)
	if err != nil {
		return 0, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	path := filepath.Join(filepath.Dir(filepath.Dir(found.Path)), ThreadOwner(threadID).dirName(), strconv.FormatInt(id, 10)+"-"+safeName(found.Name, chars))
	if info, err := os.Stat(found.Path); err == nil && info.Mode().IsRegular() {
		if err := copyFile(found.Path, path); err != nil {
			return 0, false, err
		}
	}
	_, err = q.ExecContext(ctx, "UPDATE thread_files SET path = ? WHERE id = ?", path, id)
	return id, true, err
}

func copyFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o777); err != nil {
		return err
	}
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

func DeleteOwned(ctx context.Context, q db.Querier, dir string, owner Owner) (int, error) {
	column, id := owner.where()
	var n int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM thread_files WHERE "+column+" = ?", id).Scan(&n); err != nil {
		return 0, err
	}
	if _, err := q.ExecContext(ctx, "DELETE FROM thread_files WHERE "+column+" = ?", id); err != nil {
		return 0, err
	}
	if err := os.RemoveAll(filepath.Join(dir, owner.dirName())); err != nil {
		return n, err
	}
	return n, nil
}

func DeleteOne(ctx context.Context, q db.Querier, fileID int64) (bool, error) {
	found, err := Get(ctx, q, fileID)
	if err != nil || found == nil {
		return false, err
	}
	if err := os.Remove(found.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	_, err = q.ExecContext(ctx, "DELETE FROM thread_files WHERE id = ?", fileID)
	return err == nil, err
}
