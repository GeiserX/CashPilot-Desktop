package runtime

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// An image update starts its replacement before the check period has judged it
// (imageupdate.go settle). If the app stops during that period, the replacement
// runs and the old container is still set aside, which recovery would read as a
// deploy that finished (recoverSetAside). So the update writes a mark before the
// start and takes it away once the check passed or was undone: recovery that finds
// the mark undoes the replacement and starts the old container again.
type settlingMarks struct {
	mu  sync.Mutex
	dir string
	mem map[string]bool
}

// SetSettlingDir wires in the folder that keeps the marks across a restart.
// Without one they live only in memory. Call it before the first deploy.
func (p *DockerProvider) SetSettlingDir(dir string) {
	p.settling.mu.Lock()
	defer p.settling.mu.Unlock()
	p.settling.dir = dir
}

// mark records that name's replacement is in its check period.
func (s *settlingMarks) mark(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		if s.mem == nil {
			s.mem = map[string]bool{}
		}
		s.mem[name] = true
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, name), nil, 0o600)
}

// clear takes name's mark away. No mark is not an error.
func (s *settlingMarks) clear(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		delete(s.mem, name)
		return nil
	}
	if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// pending reports whether name's replacement was left in its check period. A mark
// that cannot be read counts as there: undoing an update is the safe side.
func (s *settlingMarks) pending(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		return s.mem[name]
	}
	_, err := os.Stat(filepath.Join(s.dir, name))
	return !errors.Is(err, fs.ErrNotExist)
}
