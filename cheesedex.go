package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	_ "embed"
	"flag"
	"html"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	mdhtml "github.com/yuin/goldmark/renderer/html"
)

//go:embed index.html
var indexHTML string
var indexTmpl *template.Template

func init() {
	indexTmpl = template.New("")
	indexTmpl.Funcs(template.FuncMap{"HumanizeBytes": func(size int64) string {
		return humanize.Bytes(uint64(size))
	}})
	_, err := indexTmpl.Parse(indexHTML)
	if err != nil {
		panic(err)
	}
}

func main() {
	addr := flag.String("a", ":6060", "listen address")
	dir := flag.String("d", ".", "directory to serve")
	flag.Parse()
	err := http.ListenAndServe(*addr, &Server{*dir})
	if err != nil {
		log.Fatalln(err)
	}
}

type Server struct {
	dir string
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	basepath := path.Clean(r.URL.Path)
	p := path.Join(s.dir, basepath)
	file, err := os.Open(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if stat.IsDir() {
		if r.Method != http.MethodGet {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed),
				http.StatusMethodNotAllowed)
			return
		}
		if len(r.URL.Path) < 1 || r.URL.Path[len(r.URL.Path)-1] != '/' {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusTemporaryRedirect)
			return
		}
		if dl := r.URL.Query().Get("dl"); dl != "" {
			_, dirname := path.Split(basepath)
			if dirname == "" {
				dirname = "root"
			}
			switch dl {
			case "targz":
				w.Header().Set("Content-Disposition", "attachment; filename="+dirname+".tar.gz")
				err = archiveTarGZ(p, w)
			case "zip":
				w.Header().Set("Content-Disposition", "attachment; filename="+dirname+".zip")
				err = archiveZIP(p, w)
			default:
				http.Error(w, "dl must be one of 'targz', 'zip'", http.StatusBadRequest)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		files, err := file.Readdir(0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, file := range files {
			if file.Name() == "index.html" {
				http.ServeFile(w, r, path.Join(p, "index.html"))
				return
			}
		}
		dir := new(IndexContext)
		err = dir.Populate(files, basepath, s.dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		err = indexTmpl.Execute(w, dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		return
	}
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), file)
}

func archiveZIP(root string, wr io.Writer) error {
	w := zip.NewWriter(wr)
	fsys := os.DirFS(root)
	err := fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type() != 0 {
			return nil
		}
		f, err := fsys.Open(fpath)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = fpath
		fw, err := w.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = io.Copy(fw, f)
		return err
	})
	if err != nil {
		return err
	}
	return w.Close()
}

func archiveTarGZ(root string, wr io.Writer) error {
	zw := gzip.NewWriter(wr)
	w := tar.NewWriter(zw)
	fsys := os.DirFS(root)
	err := fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type() != 0 {
			return nil
		}
		f, err := fsys.Open(fpath)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = fpath
		err = w.WriteHeader(header)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, f)
		return err
	})
	if err != nil {
		return err
	}
	err = w.Close()
	if err != nil {
		return err
	}
	return zw.Close()
}

type IndexContext struct {
	Name   string
	Path   string
	Files  []FileInfo
	Crumbs []Crumb
	ReadMe *template.HTML
	Root   bool
	Icon   string
}

type FileInfo struct {
	fs.FileInfo
	TargetMode fs.FileMode
}

type Crumb struct {
	Link, Text string
}

func (f FileInfo) IconName() string {
	switch f.Mode().Type() {
	case fs.ModeDir:
		return "folder"
	case fs.ModeSymlink:
		if f.TargetMode.IsDir() {
			return "folder-shortcut"
		}
		return "file-shortcut"
	default:
		return "file"
	}
}

func (f FileInfo) GoesToDir() bool {
	return f.mode().IsDir()
}

func (f FileInfo) mode() fs.FileMode {
	if f.Mode().Type() == fs.ModeSymlink {
		return f.TargetMode
	}
	return f.Mode()
}

func (d *IndexContext) Populate(files []fs.FileInfo, fpath string, root string) error {
	d.Files = make([]FileInfo, len(files))
	for i, f := range files {
		d.Files[i] = FileInfo{f, 0}
		if f.Mode().Type() == fs.ModeSymlink {
			stat, err := os.Stat(path.Join(root, fpath, f.Name()))
			if err != nil {
				continue
			}
			d.Files[i].TargetMode = stat.Mode()
		}
	}
	_, d.Name = path.Split(fpath)
	sort.Slice(d.Files, func(i, j int) bool {
		var im, jm fs.FileMode = d.Files[i].mode(), d.Files[j].mode()
		if im.IsDir() == jm.IsDir() {
			return d.Files[i].Name() < d.Files[j].Name()
		}
		if im.IsDir() {
			return true
		}
		return false
	})

	d.Path = fpath
	if fpath == "/" {
		d.Root = true
	}

	split := strings.Split(fpath, "/")
	if split[len(split)-1] == "" {
		split = split[:len(split)-1]
	}
	d.Crumbs = make([]Crumb, len(split))
	for i := range d.Crumbs {
		segment := split[i]
		d.Crumbs[i] = Crumb{strings.Repeat("../", len(d.Crumbs)-i-1), segment}
	}

	for _, finfo := range d.Files {
		switch strings.ToLower(finfo.Name()) {
		case "readme.txt":
			p, err := os.ReadFile(path.Join(root, fpath, finfo.Name()))
			if err != nil {
				return err
			}
			escaped := template.HTML("<pre>" + html.EscapeString(string(p)) + "</pre>")
			d.ReadMe = &escaped
		case "readme.html":
			p, err := os.ReadFile(path.Join(root, fpath, finfo.Name()))
			if err != nil {
				return err
			}
			escaped := template.HTML(p)
			d.ReadMe = &escaped
		case "readme.md":
			p, err := os.ReadFile(path.Join(root, fpath, finfo.Name()))
			if err != nil {
				return err
			}
			sb := new(strings.Builder)
			md := goldmark.New(
				goldmark.WithExtensions(extension.GFM),
				goldmark.WithRendererOptions(mdhtml.WithUnsafe()),
			)
			err = md.Convert(p, sb)
			if err != nil {
				return err
			}
			html := template.HTML(sb.String())
			d.ReadMe = &html
		default:
			continue
		}
		break
	}
	return nil
}
