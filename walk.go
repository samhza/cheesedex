package main

import (
	"io/fs"
	"os"
	"path"
)

func WalkDir(root string, fn walkDirFunc) error {
	visited := make(map[string]struct{})
	info, err := os.Stat(root)
	if err != nil {
		err = fn(root, nil, err)
	} else {
		err = walkDir(root, &statDirEntry{info}, visited, fn)
	}
	return err
}

func walkDir(name string, d fs.DirEntry, visited map[string]struct{}, walkDirFn walkDirFunc) error {
	realname := name
	willwalk := func() bool {
		if d.IsDir() {
			_, ok := visited[name]
			return !ok
		}
		if d.Type() == fs.ModeSymlink {
			link, err := os.Readlink(name)
			if err != nil {
				return false
			} else {
				if path.IsAbs(link) {
					realname = link
				} else {
					realname = path.Join(name, link)
				}
				_, ok := visited[realname]
				if ok {
					return false
				}
				if finfo, err := os.Stat(realname); err != nil {
					return false
				} else {
					return finfo.IsDir()
				}
			}
		}
		return false
	}
	if err := walkDirFn(name, d, nil); err != nil || !willwalk() {
		return err
	}
	visited[realname] = struct{}{}

	dirs, err := os.ReadDir(name)
	if err != nil {
		err = walkDirFn(name, d, err)
		if err != nil {
			return err
		}
	}

	for _, d1 := range dirs {
		name1 := path.Join(name, d1.Name())
		if err := walkDir(name1, d1, visited, walkDirFn); err != nil {
			return err
		}
	}
	return nil
}

type walkDirFunc func(path string, d fs.DirEntry, err error) error

type statDirEntry struct {
	info fs.FileInfo
}

func (d *statDirEntry) Name() string               { return d.info.Name() }
func (d *statDirEntry) IsDir() bool                { return d.info.IsDir() }
func (d *statDirEntry) Type() fs.FileMode          { return d.info.Mode().Type() }
func (d *statDirEntry) Info() (fs.FileInfo, error) { return d.info, nil }
