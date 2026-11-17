//go:build linux || darwin

package mediaagent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var ErrStateInUse = errors.New("the media agent state directory is in use")

type state struct {
	dir     string
	lock    *os.File
	agentID string
}

func openState(dir, cache string) (*state, error) {
	for _, d := range []string{dir, cache} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return nil, err
		}
	}
	lock, err := os.OpenFile(filepath.Join(dir, "agent.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrStateInUse
		}
		return nil, err
	}
	s := &state{dir: dir, lock: lock}
	if s.agentID, err = agentID(dir); err != nil {
		_ = s.close()
		return nil, err
	}
	return s, nil
}

func (s *state) close() error {
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	s.lock = nil
	return err
}

func agentID(dir string) (string, error) {
	path := filepath.Join(dir, "agent_id")
	b, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(b))
		if raw, err := hex.DecodeString(id); err != nil || len(raw) != 16 {
			return "", errors.New("the media agent state directory has an invalid agent_id")
		}
		return id, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	id := hex.EncodeToString(raw)
	if err := writeDurable(dir, "agent_id", []byte(id+"\n")); err != nil {
		return "", fmt.Errorf("write agent_id: %w", err)
	}
	return id, nil
}

func writeDurable(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
