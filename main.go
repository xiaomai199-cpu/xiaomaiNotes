package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultAddr = ":44444"
	defaultCat  = "default"
	trashCat    = "回收站"
	trashMeta   = ".trash.json"
	orderFile   = ".category_order.json"
)

// 数据存放目录（启动时由 -data 参数 / MARKNOTES_DATA 环境变量决定）
var (
	dataRoot  string // 各用户笔记数据：<数据目录>/data/<用户名>
	usersFile string // 用户账号文件：<数据目录>/users.json
)

//go:embed templates static
var embeddedFS embed.FS

type contextKey string

const ctxUsername contextKey = "username"

var (
	sessionMu    sync.Mutex
	sessions     = map[string]sessionData{}
	usersMu      sync.RWMutex
	tmpl         *template.Template
	imageRefRe   = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)
	htmlImgRefRe = regexp.MustCompile(`(?i)<img\b[^>]*\bsrc=["']([^"']+)["'][^>]*>`)
	basePath     string
)

// ── types ────────────────────────────────────────────────────────────────────

type sessionData struct {
	Username string
	Expires  time.Time
}

type User struct {
	Username   string `json:"username"`
	PassHash   string `json:"passHash"`
	IsAdmin    bool   `json:"isAdmin"`
	SyncOffset int    `json:"syncOffset"` // 源码↔预览同步的默认行号偏移（用户级）
}

type app struct {
	basePath string
	version  string
}

type templateData struct {
	Username string
	IsAdmin  bool
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

// ── user storage ─────────────────────────────────────────────────────────────

func readUsers() ([]User, error) {
	usersMu.RLock()
	defer usersMu.RUnlock()
	b, err := os.ReadFile(usersFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []User{}, nil
		}
		return nil, err
	}
	var users []User
	if err := json.Unmarshal(b, &users); err != nil {
		return nil, err
	}
	return users, nil
}

func writeUsers(users []User) error {
	usersMu.Lock()
	defer usersMu.Unlock()
	b, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(usersFile, b, 0600)
}

func findUser(username string) (User, bool) {
	users, err := readUsers()
	if err != nil {
		return User{}, false
	}
	for _, u := range users {
		if u.Username == username {
			return u, true
		}
	}
	return User{}, false
}

func userCount() int {
	users, _ := readUsers()
	return len(users)
}

func ensureAdminUser() error {
	users, err := readUsers()
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return nil
	}
	adminPass := envDefault("ADMIN_PASS", "admin123")
	hash, err := bcrypt.GenerateFromPassword([]byte(adminPass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return writeUsers([]User{{Username: "admin", PassHash: string(hash), IsAdmin: true}})
}

// ── session ──────────────────────────────────────────────────────────────────

func newSession(username string) string {
	token := randomToken(32)
	sessionMu.Lock()
	sessions[token] = sessionData{Username: username, Expires: time.Now().Add(24 * time.Hour)}
	sessionMu.Unlock()
	return token
}

func validSession(token string) (string, bool) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	s, ok := sessions[token]
	if !ok || time.Now().After(s.Expires) {
		delete(sessions, token)
		return "", false
	}
	return s.Username, true
}

func deleteSession(token string) {
	sessionMu.Lock()
	delete(sessions, token)
	sessionMu.Unlock()
}

func currentUser(r *http.Request) string {
	u, _ := r.Context().Value(ctxUsername).(string)
	return u
}

func isAdmin(r *http.Request) bool {
	u, ok := findUser(currentUser(r))
	return ok && u.IsAdmin
}

// ── per-user data paths ───────────────────────────────────────────────────────

func userDataDir(username string) string {
	return filepath.Join(dataRoot, username)
}

func notesDir(dataDir, category string) string {
	return filepath.Join(dataDir, category, "notes")
}

func imgDir(dataDir, category string) string {
	return filepath.Join(dataDir, category, "img")
}

func notePath(dataDir, category, name string) string {
	return filepath.Join(notesDir(dataDir, category), filepath.Base(name))
}

// defaultDataDir 返回未指定 -data / MARKNOTES_DATA 时的默认数据目录：
// macOS 上为 iCloud 云盘的「科研笔记--麦子」文件夹（随 iCloud 自动同步），其他系统为当前目录。
func defaultDataDir() string {
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs", "科研笔记--麦子")
		}
	}
	return "."
}

// ── main / routes ─────────────────────────────────────────────────────────────

func main() {
	defaultData := os.Getenv("MARKNOTES_DATA")
	if defaultData == "" {
		defaultData = defaultDataDir()
	}
	dataFlag := flag.String("data", defaultData, "数据存放目录（其中保存 users.json 与 data/ 笔记数据）")
	addrFlag := flag.String("addr", envDefault("MARKNOTES_ADDR", defaultAddr), "监听地址，如 :44444")
	windowFlag := flag.Bool("window", false, "以独立窗口应用模式运行（macOS：内嵌原生 WebView 窗口，关闭窗口即退出）")
	flag.Parse()

	root, err := filepath.Abs(*dataFlag)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		log.Fatal(err)
	}
	dataRoot = filepath.Join(root, "data")
	usersFile = filepath.Join(root, "users.json")
	addr := *addrFlag

	basePath = normalizeBasePath(os.Getenv("BASE_PATH"))
	a := &app{
		basePath: basePath,
		version:  time.Now().Format("20060102150405"),
	}

	tmpl, err = template.New("").Funcs(template.FuncMap{
		"not_self": func(currentUser, rowUser string) bool { return currentUser != rowUser },
	}).ParseFS(embeddedFS, "templates/*.html")
	if err != nil {
		log.Fatal(err)
	}

	if err := ensureAdminUser(); err != nil {
		log.Fatal(err)
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

	log.Printf("Data directory: %s", root)
	log.Printf("Research notes running at http://localhost%s%s", addr, a.withBase("/"))

	if *windowFlag {
		url := localURL(addr, a.withBase("/"))
		// 端口被占用通常是已有 MarkNotes 实例在运行：直接开窗口连上它即可
		if ln, err := net.Listen("tcp", addr); err == nil {
			go func() { log.Fatal(http.Serve(ln, mux)) }()
		} else {
			log.Printf("端口已被占用（%v），连接已有实例", err)
		}
		runNativeWindow(url)
		return
	}

	log.Fatal(http.ListenAndServe(addr, mux))
}

// localURL 把监听地址转换为本机访问地址，如 ":44444" -> "http://localhost:44444/"
func localURL(addr, path string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://localhost:44444" + path
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + path
}

func (a *app) registerRoutes(mux *http.ServeMux) {
	staticFS, err := fs.Sub(embeddedFS, "static")
	if err != nil {
		log.Fatal(err)
	}
	staticFiles := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))
	mux.Handle("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		staticFiles.ServeHTTP(w, r)
	}))

	mux.HandleFunc("/login", a.login)
	mux.HandleFunc("/logout", a.logout)
	mux.HandleFunc("/", a.requireAuth(a.index))

	// notes APIs
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
	mux.HandleFunc("/api/image-delete", a.requireAuth(a.imageDelete))
	mux.HandleFunc("/api/import", a.requireAuth(a.importMarkdown))
	mux.HandleFunc("/export", a.requireAuth(a.exportMarkdown))
	mux.HandleFunc("/backup", a.requireAuth(a.backupAll))

	// per-user static files
	mux.HandleFunc("/files/", a.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		user := currentUser(r)
		http.StripPrefix("/files/", http.FileServer(http.Dir(userDataDir(user)))).ServeHTTP(w, r)
	}))

	// user self-service
	mux.HandleFunc("/api/user/password", a.requireAuth(a.changePassword))
	mux.HandleFunc("/api/user/sync-offset", a.requireAuth(a.userSyncOffset))

	// admin
	mux.HandleFunc("/admin", a.requireAuth(a.requireAdmin(a.adminPage)))
	mux.HandleFunc("/api/admin/users/create", a.requireAuth(a.requireAdmin(a.adminCreateUser)))
	mux.HandleFunc("/api/admin/users/delete", a.requireAuth(a.requireAdmin(a.adminDeleteUser)))
	mux.HandleFunc("/api/admin/users/reset-password", a.requireAuth(a.requireAdmin(a.adminResetPassword)))
}

// ── middleware ────────────────────────────────────────────────────────────────

func (a *app) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("rn_session")
		if err != nil {
			http.Redirect(w, r, a.withBase("/login"), http.StatusSeeOther)
			return
		}
		username, ok := validSession(c.Value)
		if !ok {
			http.Redirect(w, r, a.withBase("/login"), http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUsername, username)
		next(w, r.WithContext(ctx))
	}
}

func (a *app) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isAdmin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// ── auth handlers ─────────────────────────────────────────────────────────────

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		_ = tmpl.ExecuteTemplate(w, "login.html", templateData{BasePath: a.basePath, Version: a.version})
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	user, ok := findUser(username)
	if !ok || bcrypt.CompareHashAndPassword([]byte(user.PassHash), []byte(password)) != nil {
		_ = tmpl.ExecuteTemplate(w, "login.html", templateData{Error: "用户名或密码错误", BasePath: a.basePath, Version: a.version})
		return
	}

	// ensure user's data dirs exist
	dir := userDataDir(username)
	_ = ensureCategory(dir, defaultCat)
	_ = ensureCategory(dir, trashCat)
	_ = purgeTrash(dir)

	token := newSession(username)
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
		deleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "rn_session", Path: "/", MaxAge: -1})
	http.Redirect(w, r, a.withBase("/login"), http.StatusSeeOther)
}

// ── page handlers ─────────────────────────────────────────────────────────────

func (a *app) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	user := currentUser(r)
	u, _ := findUser(user)
	_ = tmpl.ExecuteTemplate(w, "index.html", templateData{
		Username: user,
		IsAdmin:  u.IsAdmin,
		BasePath: a.basePath,
		Version:  a.version,
	})
}

func (a *app) adminPage(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	u, _ := findUser(user)
	users, err := readUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type adminData struct {
		templateData
		Users []User
	}
	_ = tmpl.ExecuteTemplate(w, "admin.html", adminData{
		templateData: templateData{
			Username: user,
			IsAdmin:  u.IsAdmin,
			BasePath: a.basePath,
			Version:  a.version,
		},
		Users: users,
	})
}

// ── user management ───────────────────────────────────────────────────────────

func (a *app) changePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(payload.NewPassword) == "" {
		http.Error(w, "new password cannot be empty", http.StatusBadRequest)
		return
	}
	username := currentUser(r)
	user, ok := findUser(username)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PassHash), []byte(payload.OldPassword)) != nil {
		http.Error(w, "当前密码错误", http.StatusUnauthorized)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(payload.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	users, err := readUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i, u := range users {
		if u.Username == username {
			users[i].PassHash = string(hash)
			break
		}
	}
	if err := writeUsers(users); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// userSyncOffset 读取/保存当前用户的源码↔预览同步默认偏移
func (a *app) userSyncOffset(w http.ResponseWriter, r *http.Request) {
	username := currentUser(r)
	switch r.Method {
	case http.MethodGet:
		user, ok := findUser(username)
		if !ok {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]int{"offset": user.SyncOffset})
	case http.MethodPost:
		var payload struct {
			Offset int `json:"offset"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// 限制在合理范围，避免异常值
		if payload.Offset < -500 {
			payload.Offset = -500
		} else if payload.Offset > 500 {
			payload.Offset = 500
		}
		users, err := readUsers()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		found := false
		for i, u := range users {
			if u.Username == username {
				users[i].SyncOffset = payload.Offset
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		if err := writeUsers(users); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]int{"offset": payload.Offset})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *app) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"isAdmin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	payload.Username = strings.TrimSpace(payload.Username)
	if payload.Username == "" || strings.ContainsAny(payload.Username, "/\\:*?\"<>|") {
		http.Error(w, "invalid username", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(payload.Password) == "" {
		http.Error(w, "password cannot be empty", http.StatusBadRequest)
		return
	}
	if _, exists := findUser(payload.Username); exists {
		http.Error(w, "username already exists", http.StatusConflict)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(payload.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	users, err := readUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	users = append(users, User{Username: payload.Username, PassHash: string(hash), IsAdmin: payload.IsAdmin})
	if err := writeUsers(users); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"username": payload.Username})
}

func (a *app) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if payload.Username == currentUser(r) {
		http.Error(w, "cannot delete yourself", http.StatusBadRequest)
		return
	}
	users, err := readUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var next []User
	found := false
	for _, u := range users {
		if u.Username == payload.Username {
			found = true
			continue
		}
		next = append(next, u)
	}
	if !found {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if err := writeUsers(next); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *app) adminResetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Username    string `json:"username"`
		NewPassword string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(payload.NewPassword) == "" {
		http.Error(w, "password cannot be empty", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(payload.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	users, err := readUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	found := false
	for i, u := range users {
		if u.Username == payload.Username {
			users[i].PassHash = string(hash)
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if err := writeUsers(users); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ── note/category API handlers ────────────────────────────────────────────────

func (a *app) tree(w http.ResponseWriter, r *http.Request) {
	dir := userDataDir(currentUser(r))
	cats, err := listCategories(dir)
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
	dir := userDataDir(currentUser(r))
	if err := ensureCategory(dir, name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = appendCategoryOrder(dir, name)
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
	dir := userDataDir(currentUser(r))
	notes, err := listNotes(dir, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, note := range notes {
		if err := trashNote(dir, name, note.Name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = removeCategoryOrder(dir, name)
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
	dir := userDataDir(currentUser(r))
	if err := ensureCategory(dir, to); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	notes, err := listNotes(dir, from)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, note := range notes {
		if _, _, err := moveNoteFiles(dir, from, to, note.Name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := os.RemoveAll(filepath.Join(dir, from)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = removeCategoryOrder(dir, from)
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
	dir := userDataDir(currentUser(r))
	oldPath := filepath.Join(dir, oldName)
	newPath := filepath.Join(dir, newName)
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
	_ = renameCategoryOrder(dir, oldName, newName)
	records, err := readTrashRecords(dir)
	if err == nil {
		changed := false
		for i := range records {
			if records[i].OriginalCategory == oldName {
				records[i].OriginalCategory = newName
				changed = true
			}
		}
		if changed {
			_ = writeTrashRecords(dir, records)
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
	dir := userDataDir(currentUser(r))
	order, err := normalizedCategoryOrder(dir)
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
	if err := writeCategoryOrder(dir, order); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string][]string{"order": order})
}

func (a *app) note(w http.ResponseWriter, r *http.Request) {
	dir := userDataDir(currentUser(r))
	switch r.Method {
	case http.MethodGet:
		category, name, err := requestNote(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, err := os.ReadFile(notePath(dir, category, name))
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
		if err := ensureCategory(dir, category); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(notePath(dir, category, name), []byte(payload.Content), 0644); err != nil {
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
		if err := trashNote(dir, category, name); err != nil {
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
	dir := userDataDir(currentUser(r))
	if err := ensureCategory(dir, category); err != nil {
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
	idir := imgDir(dir, category)
	name := uniqueFileName(idir, "paste", ext)
	if err := saveUploadedFile(filepath.Join(idir, name), file); err != nil {
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
	dir := userDataDir(currentUser(r))
	cats, err := listCategories(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	images := []imageInfo{}
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
	dir := userDataDir(currentUser(r))
	if err := ensureCategory(dir, current); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	src := filepath.Join(imgDir(dir, source), name)
	if _, err := os.Stat(src); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	destName := name
	if source != current {
		dest := filepath.Join(imgDir(dir, current), destName)
		if _, err := os.Stat(dest); err == nil {
			destName = uniqueFileName(imgDir(dir, current), strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name))
			dest = filepath.Join(imgDir(dir, current), destName)
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

func (a *app) imageDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Category string `json:"category"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	category, err := cleanName(payload.Category)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := filepath.Base(strings.TrimSpace(payload.Name))
	if name == "" || name == "." || !allowedImageExt(filepath.Ext(name)) {
		http.Error(w, "invalid image name", http.StatusBadRequest)
		return
	}
	dir := userDataDir(currentUser(r))
	target := filepath.Join(imgDir(dir, category), name)
	if err := os.Remove(target); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "image not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "category": category, "name": name})
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
	dir := userDataDir(currentUser(r))
	if err := ensureCategory(dir, category); err != nil {
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
			if _, err := os.Stat(notePath(dir, category, name)); err == nil {
				name = uniqueFileName(notesDir(dir, category), strings.TrimSuffix(name, ".md"), ".md")
			}
		} else if _, err := os.Stat(notePath(dir, category, name)); err == nil {
			overwritten = true
		}
		dest := notePath(dir, category, name)
		if err := saveUploadedFile(dest, file); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		info, _ := os.Stat(dest)
		written = append(written, importResultFor(category, "notes", name, dest, sizeOf(info), overwritten))
	case ".zip":
		written, err = importZip(dir, category, file, overwrite)
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
	var notes []string
	for _, name := range r.URL.Query()["name"] {
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
	dir := userDataDir(currentUser(r))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-notes.zip"`, category))
	zw := zip.NewWriter(w)
	defer zw.Close()
	addedImages := map[string]bool{}
	for _, note := range notes {
		b, err := os.ReadFile(notePath(dir, category, note))
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
			p := filepath.Join(imgDir(dir, category), img)
			if b, err := os.ReadFile(p); err == nil {
				_ = addZipFile(zw, "img/"+img, b)
				addedImages[img] = true
			}
		}
	}
}

func (a *app) backupAll(w http.ResponseWriter, r *http.Request) {
	dir := userDataDir(currentUser(r))
	if err := os.MkdirAll(dir, 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filename := "research-notes-backup-" + time.Now().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	zw := zip.NewWriter(w)
	defer zw.Close()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
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
	dir := userDataDir(currentUser(r))
	to, name, err = moveNoteFiles(dir, from, to, name)
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
	dir := userDataDir(currentUser(r))
	oldPath := notePath(dir, category, oldName)
	newPath := notePath(dir, category, newName)
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
	dir := userDataDir(currentUser(r))
	category, restoredName, err := restoreTrashNote(dir, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"category": category, "name": restoredName})
}

// ── core business logic ───────────────────────────────────────────────────────

func moveNoteFiles(dataDir, from, to, name string) (string, string, error) {
	if err := ensureCategory(dataDir, to); err != nil {
		return "", "", err
	}
	contentBytes, err := os.ReadFile(notePath(dataDir, from, name))
	if err != nil {
		return "", "", err
	}
	content := string(contentBytes)
	replacements := map[string]string{}
	for _, img := range markdownImages(content) {
		src := filepath.Join(imgDir(dataDir, from), img)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		destName := img
		dest := filepath.Join(imgDir(dataDir, to), destName)
		if _, err := os.Stat(dest); err == nil {
			destName = uniqueFileName(imgDir(dataDir, to), strings.TrimSuffix(img, filepath.Ext(img)), filepath.Ext(img))
			dest = filepath.Join(imgDir(dataDir, to), destName)
		}
		if err := copyFile(src, dest); err != nil {
			return "", "", err
		}
		if !imageUsedByOtherNote(dataDir, from, name, img) {
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
	destNote := notePath(dataDir, to, destName)
	if _, err := os.Stat(destNote); err == nil {
		destName = uniqueFileName(notesDir(dataDir, to), strings.TrimSuffix(name, ".md"), ".md")
		destNote = notePath(dataDir, to, destName)
	}
	if err := os.WriteFile(destNote, []byte(content), 0644); err != nil {
		return "", "", err
	}
	if err := os.Remove(notePath(dataDir, from, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	return to, destName, nil
}

func listCategories(dataDir string) ([]categoryInfo, error) {
	_ = purgeTrash(dataDir)
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
		notes, err := listNotes(dataDir, name)
		if err != nil {
			return nil, err
		}
		if notes == nil {
			notes = []noteInfo{}
		}
		images, err := listImages(dataDir, name)
		if err != nil {
			return nil, err
		}
		if images == nil {
			images = []imageInfo{}
		}
		cats = append(cats, categoryInfo{Name: name, Notes: notes, Images: images})
	}
	if len(cats) == 0 {
		if err := ensureCategory(dataDir, defaultCat); err != nil {
			return nil, err
		}
		cats = append(cats, categoryInfo{Name: defaultCat, Notes: []noteInfo{}, Images: []imageInfo{}})
	}
	order, err := normalizedCategoryOrderFor(dataDir, cats)
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

func listImages(dataDir, category string) ([]imageInfo, error) {
	entries, err := os.ReadDir(imgDir(dataDir, category))
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

func listNotes(dataDir, category string) ([]noteInfo, error) {
	entries, err := os.ReadDir(notesDir(dataDir, category))
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

func ensureCategory(dataDir, category string) error {
	if _, err := cleanName(category); err != nil {
		return err
	}
	if err := os.MkdirAll(notesDir(dataDir, category), 0755); err != nil {
		return err
	}
	return os.MkdirAll(imgDir(dataDir, category), 0755)
}

// ── trash ─────────────────────────────────────────────────────────────────────

func trashNote(dataDir, category, name string) error {
	if category == trashCat {
		return nil
	}
	if err := ensureCategory(dataDir, trashCat); err != nil {
		return err
	}
	srcNote := notePath(dataDir, category, name)
	contentBytes, err := os.ReadFile(srcNote)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	content := string(contentBytes)
	record := trashRecord{
		TrashName:        uniqueFileName(notesDir(dataDir, trashCat), time.Now().Format("20060102150405")+"-"+strings.TrimSuffix(name, ".md"), ".md"),
		OriginalName:     name,
		OriginalCategory: category,
		DeletedAt:        time.Now(),
		Images:           map[string]string{},
	}
	for _, img := range markdownImages(content) {
		src := filepath.Join(imgDir(dataDir, category), img)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		trashName := img
		dest := filepath.Join(imgDir(dataDir, trashCat), trashName)
		if _, err := os.Stat(dest); err == nil {
			trashName = uniqueFileName(imgDir(dataDir, trashCat), strings.TrimSuffix(img, filepath.Ext(img)), filepath.Ext(img))
			dest = filepath.Join(imgDir(dataDir, trashCat), trashName)
		}
		if err := copyFile(src, dest); err != nil {
			return err
		}
		record.Images[img] = trashName
		content = replaceImageRef(content, img, trashName)
		if !imageUsedByOtherNote(dataDir, category, name, img) {
			_ = os.Remove(src)
		}
	}
	if err := os.WriteFile(notePath(dataDir, trashCat, record.TrashName), []byte(content), 0644); err != nil {
		return err
	}
	if err := os.Remove(srcNote); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	records, err := readTrashRecords(dataDir)
	if err != nil {
		return err
	}
	records = append(records, record)
	return writeTrashRecords(dataDir, records)
}

func restoreTrashNote(dataDir, trashName string) (string, string, error) {
	records, err := readTrashRecords(dataDir)
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
	if err := ensureCategory(dataDir, record.OriginalCategory); err != nil {
		return "", "", err
	}
	b, err := os.ReadFile(notePath(dataDir, trashCat, record.TrashName))
	if err != nil {
		return "", "", err
	}
	content := string(b)
	reverseImages := map[string]string{}
	for original, trashed := range record.Images {
		restoreName := original
		src := filepath.Join(imgDir(dataDir, trashCat), trashed)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dest := filepath.Join(imgDir(dataDir, record.OriginalCategory), restoreName)
		if _, err := os.Stat(dest); err == nil {
			restoreName = uniqueFileName(imgDir(dataDir, record.OriginalCategory), strings.TrimSuffix(original, filepath.Ext(original)), filepath.Ext(original))
			dest = filepath.Join(imgDir(dataDir, record.OriginalCategory), restoreName)
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
	destNote := notePath(dataDir, record.OriginalCategory, restoreName)
	if _, err := os.Stat(destNote); err == nil {
		restoreName = uniqueFileName(notesDir(dataDir, record.OriginalCategory), strings.TrimSuffix(record.OriginalName, ".md"), ".md")
		destNote = notePath(dataDir, record.OriginalCategory, restoreName)
	}
	if err := os.WriteFile(destNote, []byte(content), 0644); err != nil {
		return "", "", err
	}
	_ = os.Remove(notePath(dataDir, trashCat, record.TrashName))
	for _, trashed := range record.Images {
		_ = os.Remove(filepath.Join(imgDir(dataDir, trashCat), trashed))
	}
	records = append(records[:idx], records[idx+1:]...)
	if err := writeTrashRecords(dataDir, records); err != nil {
		return "", "", err
	}
	return record.OriginalCategory, restoreName, nil
}

func purgeTrash(dataDir string) error {
	records, err := readTrashRecords(dataDir)
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
		_ = os.Remove(notePath(dataDir, trashCat, record.TrashName))
		for _, trashed := range record.Images {
			_ = os.Remove(filepath.Join(imgDir(dataDir, trashCat), trashed))
		}
	}
	return writeTrashRecords(dataDir, kept)
}

func readTrashRecords(dataDir string) ([]trashRecord, error) {
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

func writeTrashRecords(dataDir string, records []trashRecord) error {
	if err := ensureCategory(dataDir, trashCat); err != nil {
		return err
	}
	b, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, trashCat, trashMeta), b, 0644)
}

// ── category order ────────────────────────────────────────────────────────────

func categoryOrderPath(dataDir string) string {
	return filepath.Join(dataDir, orderFile)
}

func readCategoryOrder(dataDir string) ([]string, error) {
	b, err := os.ReadFile(categoryOrderPath(dataDir))
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

func writeCategoryOrder(dataDir string, order []string) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(order, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(categoryOrderPath(dataDir), b, 0644)
}

func normalizedCategoryOrder(dataDir string) ([]string, error) {
	cats, err := listCategoryNames(dataDir)
	if err != nil {
		return nil, err
	}
	order, err := readCategoryOrder(dataDir)
	if err != nil {
		return nil, err
	}
	return normalizeCategoryOrder(order, cats), nil
}

func normalizedCategoryOrderFor(dataDir string, cats []categoryInfo) ([]string, error) {
	names := make([]string, 0, len(cats))
	for _, cat := range cats {
		names = append(names, cat.Name)
	}
	order, err := readCategoryOrder(dataDir)
	if err != nil {
		return nil, err
	}
	normalized := normalizeCategoryOrder(order, names)
	_ = writeCategoryOrder(dataDir, normalized)
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

func listCategoryNames(dataDir string) ([]string, error) {
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

func appendCategoryOrder(dataDir, name string) error {
	order, err := normalizedCategoryOrder(dataDir)
	if err != nil {
		return err
	}
	for _, item := range order {
		if item == name {
			return writeCategoryOrder(dataDir, order)
		}
	}
	order = append(order, name)
	return writeCategoryOrder(dataDir, order)
}

func removeCategoryOrder(dataDir, name string) error {
	order, err := readCategoryOrder(dataDir)
	if err != nil {
		return err
	}
	var next []string
	for _, item := range order {
		if item != name {
			next = append(next, item)
		}
	}
	return writeCategoryOrder(dataDir, next)
}

func renameCategoryOrder(dataDir, oldName, newName string) error {
	order, err := readCategoryOrder(dataDir)
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
	return writeCategoryOrder(dataDir, order)
}

// ── import / export ───────────────────────────────────────────────────────────

func importZip(dataDir, category string, file multipart.File, overwrite bool) ([]importResult, error) {
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
				if !overwrite && categoryExists(dataDir, mapped) {
					mapped = uniqueCategoryName(dataDir, sourceCategory)
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
		if err := ensureCategory(dataDir, targetCategory); err != nil {
			return nil, err
		}
		_ = appendCategoryOrder(dataDir, targetCategory)
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
				if _, err := os.Stat(notePath(dataDir, targetCategory, name)); err == nil {
					name = uniqueFileName(notesDir(dataDir, targetCategory), strings.TrimSuffix(name, ".md"), ".md")
				}
			} else if _, err := os.Stat(notePath(dataDir, targetCategory, name)); err == nil {
				overwritten = true
			}
			entries = append(entries, importEntry{
				category: targetCategory, kind: "notes",
				oldName: base, newName: name, data: data, overwritten: overwritten,
			})
		case kind == "img" && allowedImageExt(filepath.Ext(base)):
			name := filepath.Base(base)
			overwritten := false
			if !overwrite {
				if _, err := os.Stat(filepath.Join(imgDir(dataDir, targetCategory), name)); err == nil {
					name = uniqueFileName(imgDir(dataDir, targetCategory), strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name))
				}
			} else if _, err := os.Stat(filepath.Join(imgDir(dataDir, targetCategory), name)); err == nil {
				overwritten = true
			}
			if imageNames[targetCategory] == nil {
				imageNames[targetCategory] = map[string]string{}
			}
			imageNames[targetCategory][base] = name
			entries = append(entries, importEntry{
				category: targetCategory, kind: "img",
				oldName: base, newName: name, data: data, overwritten: overwritten,
			})
		}
	}

	var written []importResult
	for _, entry := range entries {
		switch entry.kind {
		case "img":
			dest := filepath.Join(imgDir(dataDir, entry.category), entry.newName)
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
			dest := notePath(dataDir, entry.category, entry.newName)
			if err := os.WriteFile(dest, []byte(content), 0644); err != nil {
				return nil, err
			}
			written = append(written, importResultFor(entry.category, "notes", entry.newName, dest, int64(len(content)), entry.overwritten))
		}
	}
	return written, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

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
	return strings.TrimRight(path, "/")
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

func categoryExists(dataDir, category string) bool {
	_, err := os.Stat(filepath.Join(dataDir, category))
	return err == nil
}

func uniqueCategoryName(dataDir, base string) string {
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
		if !categoryExists(dataDir, name) {
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
		Category: category, Kind: kind, Name: name,
		Path: path, AbsPath: absPath, Size: size, Overwritten: overwritten,
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
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func imageUsedByOtherNote(dataDir, category, currentNote, img string) bool {
	notes, err := listNotes(dataDir, category)
	if err != nil {
		return false
	}
	for _, n := range notes {
		if n.Name == currentNote {
			continue
		}
		b, err := os.ReadFile(notePath(dataDir, category, n.Name))
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

func randomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}
