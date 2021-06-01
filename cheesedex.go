package main

import (
	_ "embed"
	"flag"
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
	"github.com/yuin/goldmark/renderer/html"
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
		dir := new(IndexContext)
		err := dir.Populate(file, p)
		if err != nil {
			log.Fatalln(err)
		}
		if s.dir == p {
			dir.Root = true
		}
		err = indexTmpl.Execute(w, dir)
		if err != nil {
			log.Fatalln(err)
		}
		return
	}
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), file)
}

type IndexContext struct {
	Name   string
	Files  []fs.FileInfo
	ReadMe *template.HTML
	Root   bool
	Icon   string
}

func (d *IndexContext) Populate(f *os.File, fpath string) error {
	var err error
	d.Files, err = f.Readdir(0)
	if err != nil {
		return err
	}
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	d.Name = stat.Name()
	sort.Slice(d.Files, func(i, j int) bool {
		return d.Files[i].Name() < d.Files[j].Name()
	})
	for _, finfo := range d.Files {
		if strings.ToLower(finfo.Name()) != "readme.md" {
			continue
		}
		p, err := os.ReadFile(path.Join(fpath, finfo.Name()))
		if err != nil {
			return err
		}
		sb := new(strings.Builder)
		md := goldmark.New(
			goldmark.WithExtensions(extension.GFM),
			goldmark.WithRendererOptions(html.WithUnsafe()),
		)
		err = md.Convert(p, sb)
		if err != nil {
			return err
		}
		html := template.HTML(sb.String())
		d.ReadMe = &html
		break
	}
	return nil
}

func iconName(finfo os.FileInfo) (string, error) {
	switch finfo.Mode().Type() {
	}
	return "", error
}
