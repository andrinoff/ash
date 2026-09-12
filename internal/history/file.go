package history

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// How long to wait for another shell to unlock the history file before
// going ahead without the lock. The lock is only ever held for a moment.
const (
	appendLockWait  = 250 * time.Millisecond
	rewriteLockWait = 2 * time.Second
)

// openLocked opens path and takes an exclusive lock on it, waiting at most
// wait for it. If the file is replaced while it waits, it opens the new one.
// If the lock can't be had (no flock(2), or another process holds it for
// too long) the file is returned anyway: appends are atomic regardless, and
// a stuck shell mustn't hang this one. Closing the file unlocks it.
func openLocked(path string, flag int, wait time.Duration) (*os.File, error) {
	deadline := time.Now().Add(wait)
	for {
		f, err := openFile(path, flag)
		if err != nil {
			return nil, err
		}
		if !lockFile(f, time.Until(deadline)) {
			return f, nil
		}
		fi, err := f.Stat()
		cur, err2 := os.Stat(path)
		if err != nil || err2 == nil && os.SameFile(fi, cur) || err2 != nil && !errors.Is(err2, fs.ErrNotExist) {
			return f, nil
		}
		f.Close() // replaced or removed while we waited: start over
		if time.Now().After(deadline) {
			// It keeps being replaced: go ahead without the lock, with
			// the current file rather than one that is gone.
			return openFile(path, flag)
		}
	}
}

// openFile opens path, creating its directory if flag has os.O_CREATE.
func openFile(path string, flag int) (*os.File, error) {
	f, err := os.OpenFile(path, flag, 0o600)
	if errors.Is(err, fs.ErrNotExist) && flag&os.O_CREATE != 0 {
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			f, err = os.OpenFile(path, flag, 0o600)
		}
	}
	return f, err
}

// appendFile appends rec, one line, to the history file with a single
// write, creating the file with its header if needed. It returns the range
// of bytes written and the file written to.
func appendFile(path string, rec []byte) (start, end int64, fi os.FileInfo, err error) {
	f, err := openLocked(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, appendLockWait)
	if err != nil {
		return 0, 0, nil, err
	}
	defer f.Close()
	if fi, err = f.Stat(); err != nil {
		return 0, 0, nil, err
	}
	size := fi.Size()
	buf := rec
	if size == 0 {
		buf = append([]byte(header), rec...)
	} else {
		// A shell that died halfway through a write may have left a
		// partial line: end it, so that this entry isn't lost with it.
		var last [1]byte
		if _, err := f.ReadAt(last[:], size-1); err == nil && last[0] != '\n' {
			buf = append([]byte{'\n'}, rec...)
		}
	}
	n, err := f.Write(buf)
	if err != nil {
		return 0, 0, nil, err
	}
	if end, err = f.Seek(0, io.SeekCurrent); err != nil {
		end = size + int64(n)
	}
	return end - int64(n), end, fi, nil
}

// rewrite replaces the history file with the result of edit applied to its
// entries (all of them, oldest first, as in the file; none if read is
// false), unless edit reports that it changed nothing, and makes the result
// the in-memory history. The caller holds s.wmu.
func (s *Store) rewrite(read bool, edit func([]item) ([]item, bool)) error {
	if s.path == "" {
		s.mu.RLock()
		recs := s.live()
		s.mu.RUnlock()
		recs, changed := edit(recs)
		if changed {
			st := buildState(recs)
			s.mu.Lock()
			s.setState(st)
			s.mu.Unlock()
		}
		return nil
	}
	path := s.path
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p // keep a symlinked history file a symlink
	}
	f, err := openLocked(path, os.O_RDWR, rewriteLockWait)
	switch {
	case err == nil:
		defer f.Close()
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	var recs []item
	var old os.FileInfo
	if f != nil {
		if old, err = f.Stat(); err != nil {
			return err
		}
		if read {
			data, err := readAll(f, old.Size())
			if err != nil {
				return err
			}
			recs, _ = parseRecords(data)
		}
	}
	recs, changed := edit(recs)
	if !changed {
		return nil
	}
	buf := make([]byte, 0, len(header)+64*len(recs))
	buf = append(buf, header...)
	for i := range recs {
		buf = appendRecord(buf, &recs[i])
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if old != nil {
		keepOwner(tmp, old)
	}
	_, err = tmp.Write(buf)
	if err == nil {
		err = tmp.Sync()
	}
	fi, serr := tmp.Stat()
	if err == nil {
		err = serr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	st := buildState(recs)
	s.mu.Lock()
	s.setState(st)
	s.fi, s.off, s.fresh = fi, int64(len(buf)), false
	s.mu.Unlock()
	return nil
}

// keepOwner gives f, which is to replace the file old, its permissions and,
// as far as it can, its owner, as fish does: root rewriting someone's
// history (sudo keeps HOME on macOS) mustn't leave them a file they can't
// read.
func keepOwner(f *os.File, old os.FileInfo) {
	f.Chmod(old.Mode().Perm())
	if uid, gid, ok := fileOwner(old); ok {
		f.Chown(uid, gid)
	}
}

// Merge picks up the commands other shells have added to the history file
// since this store last read it, so that the history is what a shell
// started now would see. If the file has been rewritten meanwhile (by
// another shell deleting or importing commands) it is read again from
// scratch.
func (s *Store) Merge() {
	if s.path == "" {
		return
	}
	<-s.loaded
	s.wmu.Lock()
	defer s.wmu.Unlock()
	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	s.mu.RLock()
	known, off := s.fi, s.off
	s.mu.RUnlock()

	if known == nil || !os.SameFile(known, fi) || fi.Size() < off {
		data, err := readAll(f, fi.Size())
		if err != nil {
			return
		}
		recs, n := parseRecords(data)
		st := buildState(recs)
		s.mu.Lock()
		s.setState(st)
		s.fi, s.off = fi, int64(n)
		s.mu.Unlock()
		return
	}
	if fi.Size() == off {
		return
	}
	buf := make([]byte, fi.Size()-off)
	n, err := f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	recs, end := parseRecords(string(buf[:n]))
	s.mu.Lock()
	// Our own entries among these are inserted again, which moves them
	// after the other shells' ones that precede them in the file.
	for _, it := range recs {
		s.insert(it)
	}
	s.off = off + int64(end)
	s.mu.Unlock()
}

// Delete removes every occurrence of cmd from the history and its file,
// and returns how many there were.
func (s *Store) Delete(cmd string) int {
	<-s.loaded
	s.wmu.Lock()
	defer s.wmu.Unlock()
	n := 0
	err := s.rewrite(true, func(recs []item) ([]item, bool) {
		out := recs[:0]
		for _, it := range recs {
			if it.cmd == cmd {
				n++
			} else {
				out = append(out, it)
			}
		}
		return out, n > 0
	})
	if err != nil {
		n = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.index[cmd]; ok {
		// Not in the file (or it couldn't be rewritten): forget it anyway.
		s.kill(int(i))
		delete(s.index, cmd)
		n = max(n, 1)
	}
	return n
}

// Clear empties the history and its file.
func (s *Store) Clear() {
	<-s.loaded
	s.wmu.Lock()
	defer s.wmu.Unlock()
	err := s.rewrite(false, func([]item) ([]item, bool) { return nil, true })
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.setState(buildState(nil))
	}
	s.fresh = false
}
