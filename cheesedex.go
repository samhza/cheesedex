package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"embed"
	"errors"
	"flag"
	"html"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	mdhtml "github.com/yuin/goldmark/renderer/html"
)

//go:embed *.html
var tmplHtml embed.FS
var tmpl *template.Template

func init() {
	tmpl = template.New("")
	tmpl.Funcs(template.FuncMap{
		"HumanizeBytes": func(size int64) string {
			return humanize.Bytes(uint64(size))
		},
		"Crumbs": Crumbs,
	})
	_, err := tmpl.ParseFS(tmplHtml, "*")
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
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed),
			http.StatusMethodNotAllowed)
		return
	}
	relpath := path.Clean(r.URL.Path)
	file, err := os.Open(path.Join(s.dir, relpath))
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
		if query := r.URL.Query().Get("q"); query != "" {
			isregexp := r.URL.Query().Get("regexp") == "on"
			s.handleSearch(w, relpath, query, isregexp)
			return
		}
		s.handleDir(r, w, file, relpath)
		return
	}
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), file)
}

type SearchContext struct {
	Name    string
	Path    string
	Query   string
	Results <-chan FileInfo
}

func (s *Server) handleSearch(w http.ResponseWriter,
	relpath, query string, isregexp bool) {
	var exp *regexp.Regexp
	if isregexp {
		var err error
		exp, err = regexp.Compile(query)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	results := make(chan FileInfo)
	fn := func(fpath string, d fs.DirEntry, err error) error {
		switch {
		case errors.Is(err, os.ErrPermission):
		case err == nil:
		default:
			return err
		}
		if fpath == "." {
			return nil
		}
		var matched bool
		if exp != nil {
			matched = exp.MatchString(fpath)
		} else {
			matched = strings.Contains(fpath, query)
		}
		if !matched {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		var finfo FileInfo
		finfo.PopulateFrom(path.Join(s.dir, relpath, fpath), info)
		finfo.path = fpath
		results <- finfo
		return nil
	}
	go func() {
		err := WalkDir(path.Join(s.dir, relpath), fn)
		if err != nil {
			log.Println("error encountered searching:", err)
		}
		close(results)
	}()
	ctx := SearchContext{
		Name:    query,
		Path:    relpath,
		Query:   query,
		Results: results,
	}
	err := tmpl.ExecuteTemplate(w, "search.html", ctx)
	if err != nil {
		log.Println(err)
		return
	}
}

type IndexContext struct {
	Name   string
	Path   string
	Files  []FileInfo
	ReadMe *template.HTML
	Root   bool
}

// handleDir display's a directory's file index, or returns an archive
func (s *Server) handleDir(r *http.Request, w http.ResponseWriter,
	file *os.File, relpath string) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed),
			http.StatusMethodNotAllowed)
		return
	}
	if len(r.URL.Path) < 1 || r.URL.Path[len(r.URL.Path)-1] != '/' {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusTemporaryRedirect)
		return
	}
	var err error
	if dl := r.URL.Query().Get("dl"); dl != "" {
		_, dirname := path.Split(relpath)
		if dirname == "" {
			dirname = "root"
		}
		switch dl {
		case "targz":
			w.Header().Set("Content-Disposition", "attachment; filename="+dirname+".tar.gz")
			err = archiveTarGZ(path.Join(s.dir, relpath), w)
		case "zip":
			w.Header().Set("Content-Disposition", "attachment; filename="+dirname+".zip")
			err = archiveZIP(path.Join(s.dir, relpath), w)
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
			http.ServeFile(w, r, path.Join(s.dir, relpath, "index.html"))
			return
		}
	}
	dir := new(IndexContext)
	err = dir.Populate(files, relpath, s.dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = tmpl.ExecuteTemplate(w, "dir.html", dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// archiveZIP writes a zip archive of root to wr.
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

// archiveTarGZ writes a gzipped tar archive of root to wr.
func archiveTarGZ(root string, wr io.Writer) error {
	zw := gzip.NewWriter(wr)
	w := tar.NewWriter(zw)
	fsys := os.DirFS(root)
	err := fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
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

type FileInfo struct {
	fs.FileInfo
	path       string
	TargetMode fs.FileMode
}

func (f FileInfo) RelPath() string {
	if f.path != "" {
		return f.path
	}
	return f.Name()
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

func (f *FileInfo) PopulateFrom(fpath string, i fs.FileInfo) error {
	f.FileInfo = i
	if f.Mode().Type() == fs.ModeSymlink {
		stat, err := os.Stat(fpath)
		if err != nil {
			return err
		}
		f.TargetMode = stat.Mode()
	}
	return nil
}

type Crumb struct {
	Link, Text string
}

func Crumbs(dirpath string) []Crumb {
	split := strings.Split(dirpath, "/")
	if split[len(split)-1] == "" {
		split = split[:len(split)-1]
	}
	crumbs := make([]Crumb, len(split))
	for i := range crumbs {
		segment := split[i]
		crumbs[i] = Crumb{strings.Repeat("../", len(crumbs)-i-1), segment}
	}
	return crumbs
}

func (d *IndexContext) Populate(
	files []fs.FileInfo, dirpath, root string) error {
	d.Files = make([]FileInfo, len(files))
	for i, f := range files {
		d.Files[i].PopulateFrom(
			path.Join(root, dirpath, f.Name()), f)
	}
	_, d.Name = path.Split(dirpath)
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

	d.Path = dirpath
	if dirpath == "/" {
		d.Root = true
	}

	for _, finfo := range d.Files {
		switch strings.ToLower(finfo.Name()) {
		case "readme.txt":
			p, err := os.ReadFile(path.Join(root, dirpath, finfo.Name()))
			if err != nil {
				return err
			}
			escaped := template.HTML("<pre>" + html.EscapeString(string(p)) + "</pre>")
			d.ReadMe = &escaped
		case "readme.html":
			p, err := os.ReadFile(path.Join(root, dirpath, finfo.Name()))
			if err != nil {
				return err
			}
			escaped := template.HTML(p)
			d.ReadMe = &escaped
		case "readme.md":
			p, err := os.ReadFile(path.Join(root, dirpath, finfo.Name()))
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
