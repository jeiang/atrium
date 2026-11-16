package pause

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type State struct {
	Paused bool     `json:"paused"`
	By     *string  `json:"by"`
	Source *string  `json:"source"`
	Since  *float64 `json:"since"`
	Reason *string  `json:"reason"`
}

type Record struct {
	By     string  `json:"by,omitempty"`
	Source string  `json:"source,omitempty"`
	Since  float64 `json:"since,omitempty"`
	Reason string  `json:"reason,omitempty"`
}

func Paused(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func Read(path string) (State, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	out := State{Paused: true}
	var r Record
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &r)
	}
	if r.Since == 0 {
		r.Since = float64(info.ModTime().UnixNano()) / 1e9
	}
	out.Since = &r.Since
	if r.By != "" {
		out.By = &r.By
	}
	if r.Source != "" {
		out.Source = &r.Source
	}
	if r.Reason != "" {
		out.Reason = &r.Reason
	}
	return out, nil
}

func Set(path string, r Record) error {
	if Paused(path) {
		return nil
	}
	if r.Since == 0 {
		r.Since = float64(time.Now().UnixNano()) / 1e9
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.%d.tmp", path, os.Getpid(), time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

func Clear(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
