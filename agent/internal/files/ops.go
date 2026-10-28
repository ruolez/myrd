package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ruolez/remotectl/proto"
)

// File operations (feature "file_ops"): mkdir, rename and delete, answered
// with `ok` or `error` and recorded for the audit log like transfers.

const (
	OpMkdir  = "mkdir"
	OpRename = "rename"
	OpDelete = "delete"
)

var (
	errExists    = errors.New("already exists")
	errProtected = errors.New("a top-level location cannot be renamed or deleted")
)

// opResult is what an operation did, filled in as far as it got.
type opResult struct {
	path      string // the entry's path afterwards
	existed   bool   // mkdir with exist_ok found the folder
	name, dir string
	to, kind  string
}

func (s *Service) fileOp(m Msg, fn func(Msg) (opResult, error)) any {
	if s.c.AuditOK != nil && !s.c.AuditOK() {
		id := m.ID
		return errorMsg{T: "error", ID: &id, Code: CodeAuditUnavailable, Detail: "the audit log is not reachable"}
	}
	start := s.c.Now()
	r, err := fn(m)
	if s.c.Record != nil && r.name != "" && !r.existed {
		rec := proto.FileRecord{Xfer: m.ID, Direction: m.T, Name: r.name, Dir: r.dir, To: r.to, Kind: r.kind,
			Result: StateOK, DurationMS: s.c.Now().Sub(start).Milliseconds()}
		if err != nil {
			rec.Result, rec.Error = StateError, errCode(err)
		}
		s.c.Record(rec)
	}
	s.c.Log.Info("file operation", "op", m.T, "name", r.name, "dir", r.dir, "to", r.to, "error", errCode(err))
	if err != nil {
		return reqError(m.ID, err)
	}
	return okMsg{T: "ok", ID: m.ID, Path: r.path, Existed: r.existed}
}

func (s *Service) mkdir(m Msg) (opResult, error) {
	r := opResult{name: truncUTF8(m.Name, maxNameBytes), dir: truncUTF8(m.Dir, 4096), kind: KindDir}
	dir, err := CheckPath(m.Dir, s.c.goos)
	if err != nil {
		return r, err
	}
	r.dir = dir
	name, err := SanitizeName(m.Name)
	if err != nil {
		return r, err
	}
	r.name, r.path = name, JoinPath(dir, name, s.c.goos)
	err = s.c.Host.Do(func() error {
		real, err := s.resolve(dir)
		if err != nil {
			return err
		}
		target := filepath.Join(real, name)
		if s.deny.Denied(target, s.c.goos) || s.deny.Denied(r.path, s.c.goos) {
			return ErrDenied
		}
		err = os.Mkdir(target, 0o755)
		if errors.Is(err, fs.ErrExist) {
			if st, serr := os.Stat(target); serr == nil && st.IsDir() && m.ExistOK {
				r.existed = true
				return nil
			}
			return errExists
		}
		return err
	})
	return r, err
}

func (s *Service) rename(m Msg) (opResult, error) {
	r := opResult{name: truncUTF8(m.Path, 4096), to: truncUTF8(m.Name, maxNameBytes)}
	p, err := CheckPath(m.Path, s.c.goos)
	if err != nil {
		return r, err
	}
	r.dir, r.name = ParentDir(p, s.c.goos), BaseName(p, s.c.goos)
	to, err := SanitizeName(m.Name)
	if err != nil {
		return r, err
	}
	r.to, r.path = to, JoinPath(r.dir, to, s.c.goos)
	protected, err := s.protectedRoots()
	if err != nil {
		return r, err
	}
	err = s.c.Host.Do(func() error {
		from, st, err := s.entry(p, protected)
		if err != nil {
			return err
		}
		r.kind = kindOf(st)
		target := filepath.Join(filepath.Dir(from), to)
		if s.deny.Covers(target, s.c.goos) {
			return ErrDenied
		}
		if target == from {
			return nil
		}
		err = renameNoReplace(from, target)
		if errors.Is(err, fs.ErrExist) {
			// A case-only change on a case-insensitive file system.
			if t, terr := os.Lstat(target); terr == nil && os.SameFile(st, t) {
				return os.Rename(from, target)
			}
			return errExists
		}
		return err
	})
	return r, err
}

func (s *Service) deleteEntry(m Msg) (opResult, error) {
	r := opResult{name: truncUTF8(m.Path, 4096)}
	p, err := CheckPath(m.Path, s.c.goos)
	if err != nil {
		return r, err
	}
	r.dir, r.name, r.path = ParentDir(p, s.c.goos), BaseName(p, s.c.goos), p
	protected, err := s.protectedRoots()
	if err != nil {
		return r, err
	}
	err = s.c.Host.Do(func() error {
		real, st, err := s.entry(p, protected)
		if err != nil {
			return err
		}
		r.kind = kindOf(st)
		if st.IsDir() {
			return os.RemoveAll(real)
		}
		return os.Remove(real)
	})
	return r, err
}

// protectedRoots are the browse roots (home, its standard folders, volumes,
// drives): they and their ancestors are never renamed or deleted.
func (s *Service) protectedRoots() ([]string, error) {
	roots, _, err := s.c.Host.Roots()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		out = append(out, r.Path)
	}
	return out, nil
}

// entry resolves p's parent (following links) but not p itself, and refuses
// file-system roots, protected roots and anything that is or contains a
// denied path. It returns the real path and its Lstat. Call inside Host.Do.
func (s *Service) entry(p string, protected []string) (string, fs.FileInfo, error) {
	parent := ParentDir(p, s.c.goos)
	if parent == "" {
		return "", nil, errProtected
	}
	realParent, err := s.resolve(parent)
	if err != nil {
		return "", nil, err
	}
	real := filepath.Join(realParent, BaseName(p, s.c.goos))
	for _, root := range protected {
		resolved, rerr := filepath.EvalSymlinks(root)
		if within(root, p, s.c.goos) || within(root, real, s.c.goos) || rerr == nil && within(resolved, real, s.c.goos) {
			return "", nil, errProtected
		}
	}
	if s.deny.Covers(p, s.c.goos) || s.deny.Covers(real, s.c.goos) {
		return "", nil, ErrDenied
	}
	st, err := os.Lstat(real)
	if err != nil {
		return "", nil, err
	}
	return real, st, nil
}

func kindOf(st fs.FileInfo) string {
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		return KindOther
	case st.IsDir():
		return KindDir
	case st.Mode().IsRegular():
		return KindFile
	}
	return KindOther
}
