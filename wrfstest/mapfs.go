// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package wrfstest

import (
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	fs "github.com/Raytar/wrfs"
)

// A MapFS is a simple in-memory file system for use in tests,
// represented as a map from path names (arguments to Open)
// to information about the files or directories they represent.
//
// The map need not include parent directories for files contained
// in the map; those will be synthesized if needed.
// But a directory can still be included by setting the MapFile.Mode's ModeDir bit;
// this may be necessary for detailed control over the directory's FileInfo
// or to create an empty directory.
//
// File system operations read directly from the map,
// so that the file system can be changed by editing the map as needed.
// An implication is that file system operations must not run concurrently
// with changes to the map, which would be a race.
// Another implication is that opening or reading a directory requires
// iterating over the entire map, so a MapFS should typically be used with not more
// than a few hundred entries or directory reads.
type MapFS map[string]*MapFile

// A MapFile describes a single file in a MapFS.
type MapFile struct {
	Data    []byte      // file content
	Mode    fs.FileMode // FileInfo.Mode
	ModTime time.Time   // FileInfo.ModTime
	Sys     interface{} // FileInfo.Sys
}

// MapFileSys adds additional fields to a MapFile.
type MapFileSys struct {
	ATime time.Time
	Uid   int
	Gid   int
}

var _ fs.FS = MapFS(nil)
var _ fs.File = (*openMapFile)(nil)

// Open opens the named file.
func (fsys MapFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	file := fsys[name]
	if file != nil && file.Mode&fs.ModeDir == 0 {
		// Ordinary file
		return &openMapFile{name, mapFileInfo{path.Base(name), file}, 0}, nil
	}

	// Directory, possibly synthesized.
	// Note that file can be nil here: the map need not contain explicit parent directories for all its files.
	// But file can also be non-nil, in case the user wants to set metadata for the directory explicitly.
	// Either way, we need to construct the list of children of this directory.
	var list []mapFileInfo
	var elem string
	var need = make(map[string]bool)
	if name == "." {
		elem = "."
		for fname, f := range fsys {
			i := strings.Index(fname, "/")
			if i < 0 {
				list = append(list, mapFileInfo{fname, f})
			} else {
				need[fname[:i]] = true
			}
		}
	} else {
		elem = name[strings.LastIndex(name, "/")+1:]
		prefix := name + "/"
		for fname, f := range fsys {
			if strings.HasPrefix(fname, prefix) {
				felem := fname[len(prefix):]
				i := strings.Index(felem, "/")
				if i < 0 {
					list = append(list, mapFileInfo{felem, f})
				} else {
					need[fname[len(prefix):len(prefix)+i]] = true
				}
			}
		}
		// If the directory name is not in the map,
		// and there are no children of the name in the map,
		// then the directory is treated as not existing.
		if file == nil && list == nil && len(need) == 0 {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
	}
	for _, fi := range list {
		delete(need, fi.name)
	}
	for name := range need {
		list = append(list, mapFileInfo{name, &MapFile{Mode: fs.ModeDir}})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].name < list[j].name
	})

	if file == nil {
		file = &MapFile{Mode: fs.ModeDir}
	}
	return &mapDir{name, mapFileInfo{elem, file}, list, 0}, nil
}

func (fsys MapFS) OpenFile(name string, flag int, perm fs.FileMode) (fs.File, error) {
	if flag == os.O_RDONLY {
		return fsys.Open(name)
	}

	info, err := fs.Stat(fsys, name)
	if err != nil && err != fs.ErrNotExist {
		return nil, err
	}

	// For testing purposes, it's easier to not support dirs.
	if info.IsDir() {
		return nil, &fs.PathError{Op: "open", Path: name, Err: syscall.EISDIR}
	}

	var file *MapFile
	if flag&os.O_CREATE == 1 {
		if errors.Is(err, fs.ErrNotExist) {
			file = &MapFile{
				Mode:    perm,
				ModTime: time.Now(),
				Sys:     &MapFileSys{},
			}
			fsys[name] = file
		} else if flag&os.O_EXCL == 1 {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrExist}
		}
	} else {
		file = fsys[name]
	}

	if flag&os.O_TRUNC == 1 {
		file.Data = nil
	}

	ofile := &openMapFile{
		path: name,
		mapFileInfo: mapFileInfo{
			name: path.Base(name),
			f:    file,
		},
		offset: 0,
	}

	// TODO make file read-write, read-only or write-only
	return ofile, nil
}

func (fsys MapFS) Mkdir(name string, perm fs.FileMode) error {
	_, err := fs.Stat(fsys, name)
	if err == nil {
		return &fs.PathError{Op: "mkdir", Path: name, Err: fs.ErrExist}
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return &fs.PathError{Op: "mkdir", Path: name, Err: err}
	}
	fsys[name] = &MapFile{
		Mode:    fs.ModeDir | perm,
		ModTime: time.Now(),
	}
	return nil
}

func (fsys MapFS) Remove(name string) error {
	info, err := fs.Stat(fsys, name)
	if err != nil {
		return &fs.PathError{Op: "remove", Path: name, Err: err}
	}
	if info.IsDir() {
		dir, err := fs.ReadDir(fsys, name)
		if err != nil {
			return &fs.PathError{Op: "remove", Path: name, Err: err}
		}
		if len(dir) != 0 {
			return &fs.PathError{Op: "remove", Path: name, Err: syscall.ENOTEMPTY}
		}
	}
	delete(fsys, name)
	return nil
}

// fsOnly is a wrapper that hides all but the fs.FS methods,
// to avoid an infinite recursion when implementing special
// methods in terms of helpers that would use them.
// (In general, implementing these methods using the package fs helpers
// is redundant and unnecessary, but having the methods may make
// MapFS exercise more code paths when used in tests.)
type fsOnly struct{ fs.FS }

func (fsys MapFS) ReadFile(name string) ([]byte, error) {
	return fs.ReadFile(fsOnly{fsys}, name)
}

func (fsys MapFS) Stat(name string) (fs.FileInfo, error) {
	return fs.Stat(fsOnly{fsys}, name)
}

func (fsys MapFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(fsOnly{fsys}, name)
}

func (fsys MapFS) Glob(pattern string) ([]string, error) {
	return fs.Glob(fsOnly{fsys}, pattern)
}

func (fsys MapFS) Chown(name string, uid, gid int) error {
	return fs.Chown(fsOnly{fsys}, name, uid, gid)
}

func (fsys MapFS) Chmod(name string, mode fs.FileMode) error {
	return fs.Chmod(fsOnly{fsys}, name, mode)
}

func (fsys MapFS) Chtimes(name string, atime, mtime time.Time) error {
	return fs.Chtimes(fsOnly{fsys}, name, atime, mtime)
}

type mkdirOnly struct {
	fs.MkdirFS
}

func (fsys MapFS) MkdirAll(path string, perm fs.FileMode) error {
	return fs.MkdirAll(mkdirOnly{fsys}, path, perm)
}

type noSub struct {
	MapFS
}

func (noSub) Sub() {} // not the fs.SubFS signature

func (fsys MapFS) Sub(dir string) (fs.FS, error) {
	return fs.Sub(noSub{fsys}, dir)
}

// A mapFileInfo implements fs.FileInfo and fs.DirEntry for a given map file.
type mapFileInfo struct {
	name string
	f    *MapFile
}

func (i *mapFileInfo) Name() string               { return i.name }
func (i *mapFileInfo) Size() int64                { return int64(len(i.f.Data)) }
func (i *mapFileInfo) Mode() fs.FileMode          { return i.f.Mode }
func (i *mapFileInfo) Type() fs.FileMode          { return i.f.Mode.Type() }
func (i *mapFileInfo) ModTime() time.Time         { return i.f.ModTime }
func (i *mapFileInfo) IsDir() bool                { return i.f.Mode&fs.ModeDir != 0 }
func (i *mapFileInfo) Sys() interface{}           { return i.f.Sys }
func (i *mapFileInfo) Info() (fs.FileInfo, error) { return i, nil }

// An openMapFile is a regular (non-directory) fs.File open for reading.
type openMapFile struct {
	path string
	mapFileInfo
	offset int64
}

func (f *openMapFile) Stat() (fs.FileInfo, error) { return &f.mapFileInfo, nil }

func (f *openMapFile) Close() error { return nil }

func (f *openMapFile) Read(b []byte) (int, error) {
	if f.offset >= int64(len(f.f.Data)) {
		return 0, io.EOF
	}
	if f.offset < 0 {
		return 0, &fs.PathError{Op: "read", Path: f.path, Err: fs.ErrInvalid}
	}
	n := copy(b, f.f.Data[f.offset:])
	f.offset += int64(n)
	return n, nil
}

func (f *openMapFile) Write(p []byte) (n int, err error) {
	// expand file if offset is greater than data length
	if f.offset > int64(len(f.f.Data)) {
		f.f.Data = append(f.f.Data, make([]byte, f.offset-int64(len(f.f.Data)))...)
	}
	if f.Size()-f.offset > int64(len(p)) {
		p = append(p, f.f.Data[f.offset+int64(len(p)):]...)
	}
	f.f.Data = append(f.f.Data[f.offset:], p...)
	f.offset += int64(len(p))
	return n, nil
}

func (f *openMapFile) Chown(uid, gid int) error {
	sys, ok := f.f.Sys.(*MapFileSys)
	if !ok {
		return fs.ErrNotSupported
	}
	sys.Uid = uid
	sys.Gid = gid
	return nil
}

func (f *openMapFile) Chmod(mode fs.FileMode) error {
	f.f.Mode = mode & fs.ModePerm
	return nil
}

func (f *openMapFile) Chtimes(atime, mtime time.Time) error {
	sys, ok := f.f.Sys.(*MapFileSys)
	if !ok {
		return fs.ErrNotSupported
	}
	sys.ATime = atime
	f.f.ModTime = mtime
	return nil
}

func (f *openMapFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		// offset += 0
	case 1:
		offset += f.offset
	case 2:
		offset += int64(len(f.f.Data))
	}
	if offset < 0 || offset > int64(len(f.f.Data)) {
		return 0, &fs.PathError{Op: "seek", Path: f.path, Err: fs.ErrInvalid}
	}
	f.offset = offset
	return offset, nil
}

func (f *openMapFile) ReadAt(b []byte, offset int64) (int, error) {
	if offset < 0 || offset > int64(len(f.f.Data)) {
		return 0, &fs.PathError{Op: "read", Path: f.path, Err: fs.ErrInvalid}
	}
	n := copy(b, f.f.Data[offset:])
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

// A mapDir is a directory fs.File (so also an fs.ReadDirFile) open for reading.
type mapDir struct {
	path string
	mapFileInfo
	entry  []mapFileInfo
	offset int
}

func (d *mapDir) Stat() (fs.FileInfo, error) { return &d.mapFileInfo, nil }
func (d *mapDir) Close() error               { return nil }
func (d *mapDir) Read(b []byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.path, Err: fs.ErrInvalid}
}

func (d *mapDir) ReadDir(count int) ([]fs.DirEntry, error) {
	n := len(d.entry) - d.offset
	if count > 0 && n > count {
		n = count
	}
	if n == 0 && count > 0 {
		return nil, io.EOF
	}
	list := make([]fs.DirEntry, n)
	for i := range list {
		list[i] = &d.entry[d.offset+i]
	}
	d.offset += n
	return list, nil
}
