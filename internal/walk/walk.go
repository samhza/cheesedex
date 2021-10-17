package walk

import (
	"io/fs"
	"os"
	"path"
)

type WalkDirFunc func(path string, info func() (fs.FileInfo, error), err error) error

func WalkDir(root string, fn WalkDirFunc) error {
	visited := make(map[string]struct{})
	info, err := os.Stat(root)
	if err != nil {
		err = fn(root, nil, err)
	} else {
		err = walkDir(root, &statDirEntry{info}, visited, fn)
	}
	return err
}

func walkDir(name string, d fs.DirEntry, visited map[string]struct{}, fn WalkDirFunc) error {
	realname := name
	var stat fs.FileInfo
	willwalk := func() bool {
		if d.IsDir() {
			_, ok := visited[name]
			return !ok
		}
		if d.Type() == fs.ModeSymlink {
			link, err := os.Readlink(name)
			if err != nil {
				return false
			}
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
		return false
	}
	getinfo := func() (fs.FileInfo, error) {
		if stat != nil {
			return stat, nil
		}
		var err error
		stat, err = os.Stat(name)
		return stat, err
	}
	if err := fn(name, getinfo, nil); err != nil || !willwalk() {
		if err == fs.SkipDir && stat.IsDir() {
			err = nil
		}
		return err
	}
	visited[realname] = struct{}{}

	dirs, err := os.ReadDir(name)
	if err != nil {
		err = fn(name, d.Info, err)
		if err != nil {
			return err
		}
	}

	for _, d1 := range dirs {
		name1 := path.Join(name, d1.Name())
		if err := walkDir(name1, d1, visited, fn); err != nil {
			if err == fs.SkipDir {
				break
			}
			return err
		}
	}
	return nil
}

type statDirEntry struct {
	info fs.FileInfo
}

func (d *statDirEntry) Name() string               { return d.info.Name() }
func (d *statDirEntry) IsDir() bool                { return d.info.IsDir() }
func (d *statDirEntry) Type() fs.FileMode          { return d.info.Mode().Type() }
func (d *statDirEntry) Info() (fs.FileInfo, error) { return d.info, nil }
