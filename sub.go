// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package wrfs

import (
	"errors"
	"path"
)

// Sub returns an FS corresponding to the subtree rooted at fsys's dir.
//
// If fs implements SubFS, Sub calls returns fsys.Sub(dir).
// Otherwise, if dir is ".", Sub returns fsys unchanged.
// Otherwise, Sub returns a new FS implementation sub that,
// in effect, implements sub.Open(dir) as fsys.Open(path.Join(dir, name)).
// The implementation also translates calls to ReadDir, ReadFile, and Glob appropriately.
//
// Note that Sub(os.DirFS("/"), "prefix") is equivalent to os.DirFS("/prefix")
// and that neither of them guarantees to avoid operating system
// accesses outside "/prefix", because the implementation of os.DirFS
// does not check for symbolic links inside "/prefix" that point to
// other directories. That is, os.DirFS is not a general substitute for a
// chroot-style security mechanism, and Sub does not change that fact.
func Sub(fsys FS, dir string) (FS, error) {
	if !ValidPath(dir) {
		return nil, &PathError{Op: "sub", Path: dir, Err: errors.New("invalid name")}
	}
	if dir == "." {
		return fsys, nil
	}
	if fsys, ok := fsys.(SubFS); ok {
		return fsys.Sub(dir)
	}
	return &subFS{fsys, dir}, nil
}

type subFS struct {
	fsys FS
	dir  string
}

// fullName maps name to the fully-qualified name dir/name.
func (f *subFS) fullName(op string, name string) (string, error) {
	if !ValidPath(name) {
		return "", &PathError{Op: op, Path: name, Err: errors.New("invalid name")}
	}
	return path.Join(f.dir, name), nil
}

// shorten maps name, which should start with f.dir, back to the suffix after f.dir.
func (f *subFS) shorten(name string) (rel string, ok bool) {
	if name == f.dir {
		return ".", true
	}
	if len(name) >= len(f.dir)+2 && name[len(f.dir)] == '/' && name[:len(f.dir)] == f.dir {
		return name[len(f.dir)+1:], true
	}
	return "", false
}

// fixErr shortens any reported names in PathErrors by stripping dir.
func (f *subFS) fixErr(err error) error {
	if e, ok := err.(*PathError); ok {
		if short, ok := f.shorten(e.Path); ok {
			e.Path = short
		}
	}
	return err
}

func (f *subFS) Open(name string) (File, error) {
	full, err := f.fullName("open", name)
	if err != nil {
		return nil, err
	}
	file, err := f.fsys.Open(full)
	return file, f.fixErr(err)
}

func (f *subFS) ReadDir(name string) ([]DirEntry, error) {
	full, err := f.fullName("read", name)
	if err != nil {
		return nil, err
	}
	dir, err := ReadDir(f.fsys, full)
	return dir, f.fixErr(err)
}

func (f *subFS) ReadFile(name string) ([]byte, error) {
	full, err := f.fullName("read", name)
	if err != nil {
		return nil, err
	}
	data, err := ReadFile(f.fsys, full)
	return data, f.fixErr(err)
}

func (f *subFS) Glob(pattern string) ([]string, error) {
	// Check pattern is well-formed.
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, err
	}
	if pattern == "." {
		return []string{"."}, nil
	}

	full := f.dir + "/" + pattern
	list, err := Glob(f.fsys, full)
	for i, name := range list {
		name, ok := f.shorten(name)
		if !ok {
			return nil, errors.New("invalid result from inner fsys Glob: " + name + " not in " + f.dir) // can't use fmt in this package
		}
		list[i] = name
	}
	return list, f.fixErr(err)
}

func (f *subFS) OpenFile(name string, flag int, perm FileMode) (File, error) {
	full, err := f.fullName("open", name)
	if err != nil {
		return nil, err
	}
	file, err := OpenFile(f.fsys, full, flag, perm)
	return file, f.fixErr(err)
}

func (f *subFS) Chmod(name string, mode FileMode) error {
	full, err := f.fullName("chmod", name)
	if err != nil {
		return err
	}
	return f.fixErr(Chmod(f.fsys, full, mode))
}

func (f *subFS) Chown(name string, uid, gid int) error {
	full, err := f.fullName("chown", name)
	if err != nil {
		return err
	}
	return f.fixErr(Chown(f.fsys, full, uid, gid))
}

func (f *subFS) Mkdir(name string, perm FileMode) error {
	full, err := f.fullName("mkdir", name)
	if err != nil {
		return err
	}
	return f.fixErr(Mkdir(f.fsys, full, perm))
}

func (f *subFS) MkdirAll(path string, perm FileMode) error {
	full, err := f.fullName("mkdir", path)
	if err != nil {
		return err
	}
	return f.fixErr(MkdirAll(f.fsys, full, perm))
}

func (f *subFS) Remove(name string) error {
	full, err := f.fullName("remove", name)
	if err != nil {
		return err
	}
	return f.fixErr(Remove(f.fsys, full))
}

func (f *subFS) RemoveAll(name string) error {
	full, err := f.fullName("remove", name)
	if err != nil {
		return err
	}
	return f.fixErr(RemoveAll(f.fsys, full))
}
