package main

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

const (
	addr       = ":44444"
	dataDir    = "categories"
	defaultCat = "default"
	trashCat   = "回收站"
	trashMeta  = ".trash.json"
	orderFile  = ".category_order.json"
)

var (
	sessions     = map[string]time.Time{}
	sessionMu    sync.Mutex
	templates    = template.Must(template.ParseGlob("templates/*.html"))
	imageRefRe   = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)
	htmlImgRefRe = regexp.MustCompile(`(?i)<img\b[^>]*\bsrc=["']([^"']+)["'][^>]*>`)
	basePath     string
)

type app struct {
	user     string
	pass     string
	basePath string
	version  string
}

type templateData struct {
	User     string
	Error    string
	BasePath string
	Version  string
}

type categoryInfo struct {
	Name   string      `json:"name"`
	Notes  []noteInfo  `json:"notes"`
	Images []imageInfo `json:"images"`
}

type noteInfo struct {
	Name     string    `json:"name"`
	ModTime  time.Time `json:"modTime"`
	Size     int64     `json:"size"`
	Category string    `json:"category"`
}

type notePayload struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	Content  string `json:"content"`
}

type movePayload struct {
	FromCategory string `json:"fromCategory"`
	ToCategory   string `json:"toCategory"`
	Name         string `json:"name"`
}

type renamePayload struct {
	Category string `json:"category"`
	OldName  string `json:"oldName"`
	NewName  string `json:"newName"`
}

type categoryPayload struct {
	Name string `json:"name"`
}

type categoryMovePayload struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type categoryRenamePayload struct {
	OldName string `json:"oldName"`
	NewName string `json:"newName"`
}

type categoryReorderPayload struct {
	Name      string `json:"name"`
	Direction string `json:"direction"`
}

type restorePayload struct {
	Name string `json:"name"`
}

type trashRecord struct {
	TrashName        string            `json:"trashName"`
	OriginalName     string            `json:"originalName"`
	OriginalCategory string            `json:"originalCategory"`
	DeletedAt        time.Time         `json:"deletedAt"`
	Images           map[string]string `json:"images"`
}

type imageInfo struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
}

type imageRefPayload struct {
	CurrentCategory string `json:"currentCategory"`
	SourceCategory  string `json:"sourceCategory"`
	Name            string `json:"name"`
}

type importResult struct {
	Category    string `json:"category"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	AbsPath     string `json:"absPath"`
	Size        int64  `json:"size"`
	Overwritten bool   `json:"overwritten"`
}

func main() {
	basePath = normalizeBasePath(os.Getenv("BASE_PATH"))
	a := &app{
		user:     envDefault("NOTE_USER", "admin"),
		pass:     envDefault("NOTE_PASS", "admin123"),
		basePath: basePath,
		version:  time.Now().Format("20060102150405"),
	}
	if err := ensureCategory(defaultCat); err != nil {
		log.Fatal(err)
	}
	if err := ensureCategory(trashCat); err != nil {
		log.Fatal(err)
	}
	if err := purgeTrash(); err != nil {
		log.Printf("purge trash: %v", err)
	}

	submux := http.NewServeMux()
	a.registerRoutes(submux)
	mux := http.NewServeMux()
	if a.basePath == "" {
		mux.Handle("/", submux)
	} else {
		mux.HandleFunc(a.basePath, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, a.withBase("/"), http.StatusMovedPermanently)
		})
		mux.Handle(a.basePath+"/", http.StripPrefix(a.basePath, submux))
	}

	log.Printf("Research notes running at http://localhost%s%s", addr, a.withBase("/"))
	log.Fatal(http.ListenAndServe(addr, mux))
}

func (a *app) registerRoutes(mux *http.ServeMux) {
	staticFiles := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	mux.Handle("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		staticFiles.ServeHTTP(w, r)
	}))
	mux.HandleFunc("/login", a.login)
	mux.HandleFunc("/logout", a.logout)
	mux.HandleFunc("/", a.requireAuth(a.index))
	mux.HandleFunc("/api/tree", a.requireAuth(a.tree))
	mux.HandleFunc("/api/render", a.requireAuth(a.renderMarkdown))
	mux.HandleFunc("/api/category", a.requireAuth(a.createCategory))
	mux.HandleFunc("/api/category-delete", a.requireAuth(a.deleteCategory))
	mux.HandleFunc("/api/category-move", a.requireAuth(a.moveCategory))
	mux.HandleFunc("/api/category-rename", a.requireAuth(a.renameCategory))
	mux.HandleFunc("/api/category-reorder", a.requireAuth(a.reorderCategory))
	mux.HandleFunc("/api/note", a.requireAuth(a.note))
	mux.HandleFunc("/api/move", a.requireAuth(a.moveNote))
	mux.HandleFunc("/api/rename", a.requireAuth(a.renameNote))
	mux.HandleFunc("/api/restore", a.requireAuth(a.restoreNote))
	mux.HandleFunc("/api/upload-image", a.requireAuth(a.uploadImage))
	mux.HandleFunc("/api/images", a.requireAuth(a.images))
	mux.HandleFunc("/api/image-ref", a.requireAuth(a.imageRef))
	mux.HandleFunc("/api/import", a.requireAuth(a.importMarkdown))
	mux.HandleFunc("/export", a.requireAuth(a.exportMarkdown))
	mux.HandleFunc("/backup", a.requireAuth(a.backupAll))
	mux.Handle("/files/", a.requireAuth(http.StripPrefix("/files/", http.FileServer(http.Dir(dataDir))).ServeHTTP))
}

func envDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func normalizeBasePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.TrimRight(path, "/")
	return path
}

func (a *app) withBase(path string) string {
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return a.basePath + path
}

func publicURL(path string) string {
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return basePath + path
}

func (a *app) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	_ = templates.ExecuteTemplate(w, "index.html", templateData{User: a.user, BasePath: a.basePath, Version: a.version})
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		_ = templates.ExecuteTemplate(w, "login.html", templateData{BasePath: a.basePath, Version: a.version})
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if r.FormValue("username") != a.user || r.FormValue("password") != a.pass {
		_ = templates.ExecuteTemplate(w, "login.html", templateData{Error: "用户名或密码错误", BasePath: a.basePath, Version: a.version})
		return
	}
	token := randomToken(32)
	sessionMu.Lock()
	sessions[token] = time.Now().Add(24 * time.Hour)
	sessionMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     "rn_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(24 * time.Hour),
	})
	http.Redirect(w, r, a.withBase("/"), http.StatusSeeOther)
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("rn_session"); err == nil {
		sessionMu.Lock()
		delete(sessions, c.Value)
		sessionMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "rn_session", Path: "/", MaxAge: -1})
	http.Redirect(w, r, a.withBase("/login"), http.StatusSeeOther)
}

func (a *app) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("rn_session")
		if err != nil || !validSession(c.Value) {
			http.Redirect(w, r, a.withBase("/login"), http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func validSession(token string) bool {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	exp, ok := sessions[token]
	if !ok || time.Now().After(exp) {
		delete(sessions, token)
		return false
	}
	return true
}

func randomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func (a *app) tree(w http.ResponseWriter, r *http.Request) {
	cats, err := listCategories()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, cats)
}

func (a *app) renderMarkdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Markdown string `json:"markdown"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)
	var buf bytes.Buffer
	if err := md.Convert([]byte(payload.Markdown), &buf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"html": buf.String()})
}

func (a *app) createCategory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name, err := cleanName(payload.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ensureCategory(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = appendCategoryOrder(name)
	writeJSON(w, map[string]string{"name": name})
}

func (a *app) deleteCategory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload categoryPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name, err := cleanName(payload.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if name == defaultCat || name == trashCat {
		http.Error(w, "default and trash categories cannot be deleted", http.StatusBadRequest)
		return
	}
	notes, err := listNotes(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, note := range notes {
		if err := trashNote(name, note.Name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := os.RemoveAll(filepath.Join(dataDir, name)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = removeCategoryOrder(name)
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *app) moveCategory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload categoryMovePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	from, err := cleanName(payload.From)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	to, err := cleanName(payload.To)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if from == to {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	if from == defaultCat || from == trashCat || to == trashCat {
		http.Error(w, "default/trash category cannot be moved this way", http.StatusBadRequest)
		return
	}
	if err := ensureCategory(to); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	notes, err := listNotes(from)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, note := range notes {
		if _, _, err := moveNoteFiles(from, to, note.Name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := os.RemoveAll(filepath.Join(dataDir, from)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = removeCategoryOrder(from)
	writeJSON(w, map[string]string{"category": to})
}

func (a *app) renameCategory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload categoryRenamePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	oldName, err := cleanName(payload.OldName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newName, err := cleanName(payload.NewName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if oldName == newName {
		writeJSON(w, map[string]string{"name": oldName})
		return
	}
	if oldName == defaultCat || oldName == trashCat || newName == trashCat {
		http.Error(w, "default/trash category cannot be renamed", http.StatusBadRequest)
		return
	}
	oldPath := filepath.Join(dataDir, oldName)
	newPath := filepath.Join(dataDir, newName)
	if _, err := os.Stat(oldPath); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if _, err := os.Stat(newPath); err == nil {
		http.Error(w, "target category already exists", http.StatusConflict)
		return
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = renameCategoryOrder(oldName, newName)
	records, err := readTrashRecords()
	if err == nil {
		changed := false
		for i := range records {
			if records[i].OriginalCategory == oldName {
				records[i].OriginalCategory = newName
				changed = true
			}
		}
		if changed {
			_ = writeTrashRecords(records)
		}
	}
	writeJSON(w, map[string]string{"name": newName})
}

func (a *app) reorderCategory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload categoryReorderPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name, err := cleanName(payload.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if payload.Direction != "up" && payload.Direction != "down" {
		http.Error(w, "direction must be up or down", http.StatusBadRequest)
		return
	}
	order, err := normalizedCategoryOrder()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	idx := -1
	for i, item := range order {
		if item == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		http.Error(w, "category not found", http.StatusNotFound)
		return
	}
	next := idx
	if payload.Direction == "up" {
		next = idx - 1
	} else {
		next = idx + 1
	}
	if next < 0 || next >= len(order) {
		writeJSON(w, map[string][]string{"order": order})
		return
	}
	order[idx], order[next] = order[next], order[idx]
	if err := writeCategoryOrder(order); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string][]string{"order": order})
}

func (a *app) note(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		category, name, err := requestNote(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, err := os.ReadFile(notePath(category, name))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{"category": category, "name": name, "content": string(b)})
	case http.MethodPost:
		var payload notePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		category, err := cleanName(payload.Category)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name, err := cleanMarkdownName(payload.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := ensureCategory(category); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(notePath(category, name), []byte(payload.Content), 0644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"category": category, "name": name})
	case http.MethodDelete:
		category, name, err := requestNote(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := trashNote(category, name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *app) uploadImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	category, err := cleanName(r.FormValue("category"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ensureCategory(category); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext == "" {
		ext = extFromContentType(header.Header.Get("Content-Type"))
	}
	if !allowedImageExt(ext) {
		http.Error(w, "unsupported image type", http.StatusBadRequest)
		return
	}
	name := uniqueFileName(imgDir(category), "paste", ext)
	if err := saveUploadedFile(filepath.Join(imgDir(category), name), file); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{
		"name": name,
		"path": "../img/" + name,
		"url":  publicURL("/files/" + category + "/img/" + name),
	})
}

func (a *app) images(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cats, err := listCategories()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var images []imageInfo
	for _, cat := range cats {
		images = append(images, cat.Images...)
	}
	sort.Slice(images, func(i, j int) bool {
		if images[i].Category == images[j].Category {
			return images[i].Name < images[j].Name
		}
		return images[i].Category < images[j].Category
	})
	writeJSON(w, images)
}

func (a *app) imageRef(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload imageRefPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	current, err := cleanName(payload.CurrentCategory)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	source, err := cleanName(payload.SourceCategory)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := filepath.Base(strings.TrimSpace(payload.Name))
	if name == "" || !allowedImageExt(filepath.Ext(name)) {
		http.Error(w, "invalid image name", http.StatusBadRequest)
		return
	}
	if err := ensureCategory(current); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	src := filepath.Join(imgDir(source), name)
	if _, err := os.Stat(src); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	destName := name
	if source != current {
		dest := filepath.Join(imgDir(current), destName)
		if _, err := os.Stat(dest); err == nil {
			destName = uniqueFileName(imgDir(current), strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name))
			dest = filepath.Join(imgDir(current), destName)
		}
		if err := copyFile(src, dest); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, map[string]string{
		"name": destName,
		"path": "../img/" + destName,
		"url":  publicURL("/files/" + current + "/img/" + destName),
	})
}

func (a *app) importMarkdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(128 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	category, err := cleanName(r.FormValue("category"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ensureCategory(category); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	overwrite := strings.EqualFold(r.FormValue("overwrite"), "true") || r.FormValue("overwrite") == "1"
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	var written []importResult
	switch ext {
	case ".md", ".markdown":
		name, err := cleanMarkdownName(header.Filename)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		overwritten := false
		if !overwrite {
			if _, err := os.Stat(notePath(category, name)); err == nil {
				name = uniqueFileName(notesDir(category), strings.TrimSuffix(name, ".md"), ".md")
			}
		} else if _, err := os.Stat(notePath(category, name)); err == nil {
			overwritten = true
		}
		dest := notePath(category, name)
		if err := saveUploadedFile(dest, file); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		info, _ := os.Stat(dest)
		written = append(written, importResultFor(category, "notes", name, dest, sizeOf(info), overwritten))
	case ".zip":
		written, err = importZip(category, file, overwrite)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "only .md or exported .zip is supported", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "written": written})
}

func (a *app) exportMarkdown(w http.ResponseWriter, r *http.Request) {
	category, err := cleanName(r.URL.Query().Get("category"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	query := r.URL.Query()
	names := query["name"]
	var notes []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		cleaned, err := cleanMarkdownName(name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		notes = append(notes, cleaned)
	}
	if len(notes) == 0 {
		http.Error(w, "select markdown files to export", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-notes.zip"`, category))
	zw := zip.NewWriter(w)
	defer zw.Close()
	addedImages := map[string]bool{}
	for _, note := range notes {
		b, err := os.ReadFile(notePath(category, note))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err := addZipFile(zw, "notes/"+note, b); err != nil {
			return
		}
		for _, img := range markdownImages(string(b)) {
			if addedImages[img] {
				continue
			}
			p := filepath.Join(imgDir(category), img)
			if b, err := os.ReadFile(p); err == nil {
				_ = addZipFile(zw, "img/"+img, b)
				addedImages[img] = true
			}
		}
	}
}

func (a *app) backupAll(w http.ResponseWriter, r *http.Request) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filename := "research-notes-backup-" + time.Now().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	zw := zip.NewWriter(w)
	defer zw.Close()
	err := filepath.WalkDir(dataDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(filepath.Join("categories", rel))
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return addZipFile(zw, rel, b)
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *app) moveNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload movePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	from, err := cleanName(payload.FromCategory)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	to, err := cleanName(payload.ToCategory)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name, err := cleanMarkdownName(payload.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if from == to {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	to, name, err = moveNoteFiles(from, to, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"category": to, "name": name})
}

func (a *app) renameNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload renamePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	category, err := cleanName(payload.Category)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	oldName, err := cleanMarkdownName(payload.OldName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newName, err := cleanMarkdownName(payload.NewName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if oldName == newName {
		writeJSON(w, map[string]string{"category": category, "name": oldName})
		return
	}
	oldPath := notePath(category, oldName)
	newPath := notePath(category, newName)
	if _, err := os.Stat(oldPath); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if _, err := os.Stat(newPath); err == nil {
		http.Error(w, "target markdown already exists", http.StatusConflict)
		return
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"category": category, "name": newName})
}

func (a *app) restoreNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload restorePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name, err := cleanMarkdownName(payload.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	category, restoredName, err := restoreTrashNote(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"category": category, "name": restoredName})
}

func moveNoteFiles(from, to, name string) (string, string, error) {
	if err := ensureCategory(to); err != nil {
		return "", "", err
	}
	contentBytes, err := os.ReadFile(notePath(from, name))
	if err != nil {
		return "", "", err
	}
	content := string(contentBytes)
	replacements := map[string]string{}
	for _, img := range markdownImages(content) {
		src := filepath.Join(imgDir(from), img)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		destName := img
		dest := filepath.Join(imgDir(to), destName)
		if _, err := os.Stat(dest); err == nil {
			destName = uniqueFileName(imgDir(to), strings.TrimSuffix(img, filepath.Ext(img)), filepath.Ext(img))
			dest = filepath.Join(imgDir(to), destName)
		}
		if err := copyFile(src, dest); err != nil {
			return "", "", err
		}
		if !imageUsedByOtherNote(from, name, img) {
			_ = os.Remove(src)
		}
		if destName != img {
			replacements["../img/"+img] = "../img/" + destName
			replacements["img/"+img] = "../img/" + destName
			replacements[img] = destName
		}
	}
	for old, newValue := range replacements {
		content = strings.ReplaceAll(content, old, newValue)
	}
	destName := name
	destNote := notePath(to, destName)
	if _, err := os.Stat(destNote); err == nil {
		destName = uniqueFileName(notesDir(to), strings.TrimSuffix(name, ".md"), ".md")
		destNote = notePath(to, destName)
	}
	if err := os.WriteFile(destNote, []byte(content), 0644); err != nil {
		return "", "", err
	}
	if err := os.Remove(notePath(from, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	return to, destName, nil
}

func listCategories() ([]categoryInfo, error) {
	_ = purgeTrash()
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, err
	}
	var cats []categoryInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		notes, err := listNotes(name)
		if err != nil {
			return nil, err
		}
		if notes == nil {
			notes = []noteInfo{}
		}
		images, err := listImages(name)
		if err != nil {
			return nil, err
		}
		if images == nil {
			images = []imageInfo{}
		}
		cats = append(cats, categoryInfo{Name: name, Notes: notes, Images: images})
	}
	if len(cats) == 0 {
		if err := ensureCategory(defaultCat); err != nil {
			return nil, err
		}
		cats = append(cats, categoryInfo{Name: defaultCat, Notes: []noteInfo{}, Images: []imageInfo{}})
	}
	order, err := normalizedCategoryOrderFor(cats)
	if err == nil {
		index := map[string]int{}
		for i, name := range order {
			index[name] = i
		}
		sort.SliceStable(cats, func(i, j int) bool {
			ii, iok := index[cats[i].Name]
			jj, jok := index[cats[j].Name]
			if iok && jok {
				return ii < jj
			}
			if iok != jok {
				return iok
			}
			return cats[i].Name < cats[j].Name
		})
	}
	return cats, nil
}

func listImages(category string) ([]imageInfo, error) {
	entries, err := os.ReadDir(imgDir(category))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var images []imageInfo
	for _, e := range entries {
		if e.IsDir() || !allowedImageExt(filepath.Ext(e.Name())) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		images = append(images, imageInfo{
			Category: category,
			Name:     e.Name(),
			URL:      publicURL("/files/" + category + "/img/" + e.Name()),
			Path:     "../img/" + e.Name(),
			Size:     info.Size(),
		})
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Name < images[j].Name })
	return images, nil
}

func listNotes(category string) ([]noteInfo, error) {
	entries, err := os.ReadDir(notesDir(category))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var notes []noteInfo
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		notes = append(notes, noteInfo{Name: e.Name(), Category: category, ModTime: info.ModTime(), Size: info.Size()})
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Name < notes[j].Name })
	return notes, nil
}

func ensureCategory(category string) error {
	if _, err := cleanName(category); err != nil {
		return err
	}
	if err := os.MkdirAll(notesDir(category), 0755); err != nil {
		return err
	}
	return os.MkdirAll(imgDir(category), 0755)
}

func notesDir(category string) string {
	return filepath.Join(dataDir, category, "notes")
}

func imgDir(category string) string {
	return filepath.Join(dataDir, category, "img")
}

func notePath(category, name string) string {
	return filepath.Join(notesDir(category), filepath.Base(name))
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultCat
	}
	name = strings.ReplaceAll(name, "\\", "-")
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.Trim(name, ". ")
	if name == "" || name == "." || name == ".." {
		return "", errors.New("invalid name")
	}
	return name, nil
}

func cleanMarkdownName(name string) (string, error) {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		return "", errors.New("invalid markdown name")
	}
	name = strings.TrimSuffix(name, ".markdown")
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		name += ".md"
	}
	return name, nil
}

func requestNote(r *http.Request) (string, string, error) {
	category, err := cleanName(r.URL.Query().Get("category"))
	if err != nil {
		return "", "", err
	}
	name, err := cleanMarkdownName(r.URL.Query().Get("name"))
	if err != nil {
		return "", "", err
	}
	return category, name, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func saveUploadedFile(path string, src multipart.File) error {
	dst, err := os.Create(path)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return err
}

func uniqueFileName(dir, prefix, ext string) string {
	prefix = strings.Trim(filepath.Base(prefix), ". ")
	if prefix == "" {
		prefix = "file"
	}
	ext = strings.ToLower(ext)
	for i := 0; ; i++ {
		suffix := time.Now().Format("20060102150405")
		if i > 0 {
			suffix = fmt.Sprintf("%s-%d", suffix, i)
		}
		name := prefix + "-" + suffix + ext
		if _, err := os.Stat(filepath.Join(dir, name)); errors.Is(err, os.ErrNotExist) {
			return name
		}
	}
}

func allowedImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		return true
	default:
		return false
	}
}

func extFromContentType(ct string) string {
	switch strings.ToLower(strings.Split(ct, ";")[0]) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	default:
		return ".png"
	}
}

func markdownImages(content string) []string {
	seen := map[string]bool{}
	var images []string
	add := func(ref string) {
		ref = strings.Trim(ref, ` "'`)
		if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "data:") {
			return
		}
		ref = strings.Split(ref, "#")[0]
		ref = strings.Split(ref, "?")[0]
		name := filepath.Base(ref)
		if name == "." || name == "" || seen[name] {
			return
		}
		seen[name] = true
		images = append(images, name)
	}
	for _, m := range imageRefRe.FindAllStringSubmatch(content, -1) {
		add(m[1])
	}
	for _, m := range htmlImgRefRe.FindAllStringSubmatch(content, -1) {
		add(m[1])
	}
	return images
}

func importZip(category string, file multipart.File, overwrite bool) ([]importResult, error) {
	type importEntry struct {
		category    string
		kind        string
		oldName     string
		newName     string
		data        []byte
		overwritten bool
	}

	tmp, err := os.CreateTemp("", "notes-import-*.zip")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	zr, err := zip.OpenReader(tmp.Name())
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	categoryTargets := map[string]string{}
	imageNames := map[string]map[string]string{}
	var entries []importEntry
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(f.Name))
		targetCategory := category
		kind := ""
		base := filepath.Base(clean)
		parts := strings.Split(clean, "/")
		if categoryIndex := zipPathSegment(parts, "categories"); categoryIndex >= 0 {
			if len(parts) < categoryIndex+4 {
				continue
			}
			sourceCategory, err := cleanName(parts[categoryIndex+1])
			if err != nil {
				return nil, err
			}
			mapped, ok := categoryTargets[sourceCategory]
			if !ok {
				mapped = sourceCategory
				if !overwrite && categoryExists(mapped) {
					mapped = uniqueCategoryName(sourceCategory)
				}
				categoryTargets[sourceCategory] = mapped
			}
			targetCategory = mapped
			kind = parts[categoryIndex+2]
		} else if notesIndex := zipPathSegment(parts, "notes"); notesIndex >= 0 && len(parts) > notesIndex+1 {
			kind = "notes"
		} else if imgIndex := zipPathSegment(parts, "img"); imgIndex >= 0 && len(parts) > imgIndex+1 {
			kind = "img"
		} else {
			continue
		}
		if err := ensureCategory(targetCategory); err != nil {
			return nil, err
		}
		_ = appendCategoryOrder(targetCategory)
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case kind == "notes" && strings.EqualFold(filepath.Ext(base), ".md"):
			name, err := cleanMarkdownName(base)
			if err != nil {
				return nil, err
			}
			overwritten := false
			if !overwrite {
				if _, err := os.Stat(notePath(targetCategory, name)); err == nil {
					name = uniqueFileName(notesDir(targetCategory), strings.TrimSuffix(name, ".md"), ".md")
				}
			} else if _, err := os.Stat(notePath(targetCategory, name)); err == nil {
				overwritten = true
			}
			entries = append(entries, importEntry{
				category:    targetCategory,
				kind:        "notes",
				oldName:     base,
				newName:     name,
				data:        data,
				overwritten: overwritten,
			})
		case kind == "img" && allowedImageExt(filepath.Ext(base)):
			name := filepath.Base(base)
			overwritten := false
			if !overwrite {
				if _, err := os.Stat(filepath.Join(imgDir(targetCategory), name)); err == nil {
					name = uniqueFileName(imgDir(targetCategory), strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name))
				}
			} else if _, err := os.Stat(filepath.Join(imgDir(targetCategory), name)); err == nil {
				overwritten = true
			}
			if imageNames[targetCategory] == nil {
				imageNames[targetCategory] = map[string]string{}
			}
			imageNames[targetCategory][base] = name
			entries = append(entries, importEntry{
				category:    targetCategory,
				kind:        "img",
				oldName:     base,
				newName:     name,
				data:        data,
				overwritten: overwritten,
			})
		default:
			continue
		}
	}
	var written []importResult
	for _, entry := range entries {
		switch entry.kind {
		case "img":
			dest := filepath.Join(imgDir(entry.category), entry.newName)
			if err := os.WriteFile(dest, entry.data, 0644); err != nil {
				return nil, err
			}
			written = append(written, importResultFor(entry.category, "img", entry.newName, dest, int64(len(entry.data)), entry.overwritten))
		case "notes":
			content := string(entry.data)
			for oldName, newName := range imageNames[entry.category] {
				if oldName != newName {
					content = replaceImageRef(content, oldName, newName)
				}
			}
			dest := notePath(entry.category, entry.newName)
			if err := os.WriteFile(dest, []byte(content), 0644); err != nil {
				return nil, err
			}
			written = append(written, importResultFor(entry.category, "notes", entry.newName, dest, int64(len(content)), entry.overwritten))
		}
	}
	return written, nil
}

func categoryExists(category string) bool {
	_, err := os.Stat(filepath.Join(dataDir, category))
	return err == nil
}

func uniqueCategoryName(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "imported"
	}
	for i := 0; ; i++ {
		suffix := time.Now().Format("20060102150405")
		name := base + "-导入-" + suffix
		if i > 0 {
			name = fmt.Sprintf("%s-%d", name, i)
		}
		if !categoryExists(name) {
			return name
		}
	}
}

func importResultFor(category, kind, name, path string, size int64, overwritten bool) importResult {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	return importResult{
		Category:    category,
		Kind:        kind,
		Name:        name,
		Path:        path,
		AbsPath:     absPath,
		Size:        size,
		Overwritten: overwritten,
	}
}

func zipPathSegment(parts []string, name string) int {
	for i, part := range parts {
		if part == name {
			return i
		}
	}
	return -1
}

func sizeOf(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	return info.Size()
}

func copyReader(dest string, src io.Reader) error {
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, src)
	return err
}

func addZipFile(zw *zip.Writer, name string, data []byte) error {
	h := &zip.FileHeader{Name: name, Method: zip.Deflate}
	h.SetModTime(time.Now())
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return copyReader(dest, in)
}

func imageUsedByOtherNote(category, currentNote, img string) bool {
	notes, err := listNotes(category)
	if err != nil {
		return false
	}
	for _, n := range notes {
		if n.Name == currentNote {
			continue
		}
		b, err := os.ReadFile(notePath(category, n.Name))
		if err != nil {
			continue
		}
		for _, ref := range markdownImages(string(b)) {
			if ref == img {
				return true
			}
		}
	}
	return false
}

func trashNote(category, name string) error {
	if category == trashCat {
		return nil
	}
	if err := ensureCategory(trashCat); err != nil {
		return err
	}
	srcNote := notePath(category, name)
	contentBytes, err := os.ReadFile(srcNote)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	content := string(contentBytes)
	record := trashRecord{
		TrashName:        uniqueFileName(notesDir(trashCat), time.Now().Format("20060102150405")+"-"+strings.TrimSuffix(name, ".md"), ".md"),
		OriginalName:     name,
		OriginalCategory: category,
		DeletedAt:        time.Now(),
		Images:           map[string]string{},
	}
	for _, img := range markdownImages(content) {
		src := filepath.Join(imgDir(category), img)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		trashName := img
		dest := filepath.Join(imgDir(trashCat), trashName)
		if _, err := os.Stat(dest); err == nil {
			trashName = uniqueFileName(imgDir(trashCat), strings.TrimSuffix(img, filepath.Ext(img)), filepath.Ext(img))
			dest = filepath.Join(imgDir(trashCat), trashName)
		}
		if err := copyFile(src, dest); err != nil {
			return err
		}
		record.Images[img] = trashName
		content = replaceImageRef(content, img, trashName)
		if !imageUsedByOtherNote(category, name, img) {
			_ = os.Remove(src)
		}
	}
	if err := os.WriteFile(notePath(trashCat, record.TrashName), []byte(content), 0644); err != nil {
		return err
	}
	if err := os.Remove(srcNote); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	records, err := readTrashRecords()
	if err != nil {
		return err
	}
	records = append(records, record)
	return writeTrashRecords(records)
}

func restoreTrashNote(trashName string) (string, string, error) {
	records, err := readTrashRecords()
	if err != nil {
		return "", "", err
	}
	idx := -1
	var record trashRecord
	for i, item := range records {
		if item.TrashName == trashName {
			idx = i
			record = item
			break
		}
	}
	if idx == -1 {
		return "", "", errors.New("trash record not found")
	}
	if err := ensureCategory(record.OriginalCategory); err != nil {
		return "", "", err
	}
	b, err := os.ReadFile(notePath(trashCat, record.TrashName))
	if err != nil {
		return "", "", err
	}
	content := string(b)
	reverseImages := map[string]string{}
	for original, trashed := range record.Images {
		restoreName := original
		src := filepath.Join(imgDir(trashCat), trashed)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dest := filepath.Join(imgDir(record.OriginalCategory), restoreName)
		if _, err := os.Stat(dest); err == nil {
			restoreName = uniqueFileName(imgDir(record.OriginalCategory), strings.TrimSuffix(original, filepath.Ext(original)), filepath.Ext(original))
			dest = filepath.Join(imgDir(record.OriginalCategory), restoreName)
		}
		if err := copyFile(src, dest); err != nil {
			return "", "", err
		}
		reverseImages[trashed] = restoreName
	}
	for trashed, restored := range reverseImages {
		content = replaceImageRef(content, trashed, restored)
	}
	restoreName := record.OriginalName
	destNote := notePath(record.OriginalCategory, restoreName)
	if _, err := os.Stat(destNote); err == nil {
		restoreName = uniqueFileName(notesDir(record.OriginalCategory), strings.TrimSuffix(record.OriginalName, ".md"), ".md")
		destNote = notePath(record.OriginalCategory, restoreName)
	}
	if err := os.WriteFile(destNote, []byte(content), 0644); err != nil {
		return "", "", err
	}
	_ = os.Remove(notePath(trashCat, record.TrashName))
	for _, trashed := range record.Images {
		_ = os.Remove(filepath.Join(imgDir(trashCat), trashed))
	}
	records = append(records[:idx], records[idx+1:]...)
	if err := writeTrashRecords(records); err != nil {
		return "", "", err
	}
	return record.OriginalCategory, restoreName, nil
}

func purgeTrash() error {
	records, err := readTrashRecords()
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	var kept []trashRecord
	for _, record := range records {
		if record.DeletedAt.After(cutoff) {
			kept = append(kept, record)
			continue
		}
		_ = os.Remove(notePath(trashCat, record.TrashName))
		for _, trashed := range record.Images {
			_ = os.Remove(filepath.Join(imgDir(trashCat), trashed))
		}
	}
	return writeTrashRecords(kept)
}

func readTrashRecords() ([]trashRecord, error) {
	path := filepath.Join(dataDir, trashCat, trashMeta)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []trashRecord{}, nil
		}
		return nil, err
	}
	var records []trashRecord
	if err := json.Unmarshal(b, &records); err != nil {
		return nil, err
	}
	return records, nil
}

func writeTrashRecords(records []trashRecord) error {
	if err := ensureCategory(trashCat); err != nil {
		return err
	}
	b, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, trashCat, trashMeta), b, 0644)
}

func categoryOrderPath() string {
	return filepath.Join(dataDir, orderFile)
}

func readCategoryOrder() ([]string, error) {
	b, err := os.ReadFile(categoryOrderPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	var order []string
	if err := json.Unmarshal(b, &order); err != nil {
		return nil, err
	}
	return order, nil
}

func writeCategoryOrder(order []string) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(order, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(categoryOrderPath(), b, 0644)
}

func normalizedCategoryOrder() ([]string, error) {
	cats, err := listCategoryNames()
	if err != nil {
		return nil, err
	}
	order, err := readCategoryOrder()
	if err != nil {
		return nil, err
	}
	return normalizeCategoryOrder(order, cats), nil
}

func normalizedCategoryOrderFor(cats []categoryInfo) ([]string, error) {
	names := make([]string, 0, len(cats))
	for _, cat := range cats {
		names = append(names, cat.Name)
	}
	order, err := readCategoryOrder()
	if err != nil {
		return nil, err
	}
	normalized := normalizeCategoryOrder(order, names)
	_ = writeCategoryOrder(normalized)
	return normalized, nil
}

func normalizeCategoryOrder(order, names []string) []string {
	exists := map[string]bool{}
	for _, name := range names {
		exists[name] = true
	}
	seen := map[string]bool{}
	var normalized []string
	for _, name := range order {
		if exists[name] && !seen[name] {
			normalized = append(normalized, name)
			seen[name] = true
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if !seen[name] {
			normalized = append(normalized, name)
		}
	}
	return normalized
}

func listCategoryNames() ([]string, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

func appendCategoryOrder(name string) error {
	order, err := normalizedCategoryOrder()
	if err != nil {
		return err
	}
	for _, item := range order {
		if item == name {
			return writeCategoryOrder(order)
		}
	}
	order = append(order, name)
	return writeCategoryOrder(order)
}

func removeCategoryOrder(name string) error {
	order, err := readCategoryOrder()
	if err != nil {
		return err
	}
	var next []string
	for _, item := range order {
		if item != name {
			next = append(next, item)
		}
	}
	return writeCategoryOrder(next)
}

func renameCategoryOrder(oldName, newName string) error {
	order, err := readCategoryOrder()
	if err != nil {
		return err
	}
	found := false
	for i, item := range order {
		if item == oldName {
			order[i] = newName
			found = true
		}
	}
	if !found {
		order = append(order, newName)
	}
	return writeCategoryOrder(order)
}

func replaceImageRef(content, oldName, newName string) string {
	replacements := map[string]string{
		"../img/" + oldName: "../img/" + newName,
		"img/" + oldName:    "../img/" + newName,
	}
	for oldValue, newValue := range replacements {
		content = strings.ReplaceAll(content, oldValue, newValue)
	}
	return content
}
