package main

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var (
	db             *sql.DB
	jwtSecret      = []byte("your-secret-key-change-in-production")
	uploadDir      = "./uploads"
	uploadProgress = make(map[string]*UploadProgress)
	progressMutex  = &sync.RWMutex{}
)

type UploadProgress struct {
	TotalSize    int64     `json:"total_size"`
	Uploaded     int64     `json:"uploaded"`
	StartTime    time.Time `json:"start_time"`
	FileName     string    `json:"file_name"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message,omitempty"`
}

type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Created  string `json:"created"`
}

type ProgressReader struct {
	Reader     io.Reader
	OnProgress func(int64)
	read       int64
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	pr.read += int64(n)
	if pr.OnProgress != nil {
		pr.OnProgress(pr.read)
	}
	return n, err
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func main() {
	initDB()
	defer db.Close()
	os.MkdirAll(uploadDir, 0755)

	http.HandleFunc("/api/register", corsMiddleware(handleRegister))
	http.HandleFunc("/api/login", corsMiddleware(handleLogin))
	http.HandleFunc("/api/user", corsMiddleware(authMiddleware(handleUser)))
	http.HandleFunc("/api/users", corsMiddleware(adminMiddleware(handleUsers)))
	http.HandleFunc("/api/users/", corsMiddleware(adminMiddleware(handleUserOps)))
	http.HandleFunc("/api/resources", corsMiddleware(handleResources))
	http.HandleFunc("/api/resources/", corsMiddleware(handleResourceOps))
	http.HandleFunc("/api/upload", corsMiddleware(authMiddleware(handleUpload)))
	http.HandleFunc("/api/upload/progress/", corsMiddleware(authMiddleware(handleUploadProgress)))
	http.HandleFunc("/api/download/", corsMiddleware(handleDownload))
	http.HandleFunc("/api/preview/", corsMiddleware(handlePreview))
	http.HandleFunc("/api/categories", corsMiddleware(handleCategories))
	http.HandleFunc("/api/announcements", corsMiddleware(handleAnnouncements))
	http.HandleFunc("/api/announcements/", corsMiddleware(adminMiddleware(handleAnnouncementOps)))
	http.HandleFunc("/api/stats", corsMiddleware(handleStats))

	fmt.Println("Server starting on :8080")
	http.ListenAndServe(":8080", nil)
}

func initDB() {
	var err error
	dbHost := getEnv("DB_HOST", "mysql")
	dbPort := getEnv("DB_PORT", "3306")
	dbUser := getEnv("DB_USER", "root")
	dbPassword := getEnv("DB_PASSWORD", "5210")
	dbName := getEnv("DB_NAME", "resource_share")

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&charset=utf8mb4&collation=utf8mb4_unicode_ci",
		dbUser, dbPassword, dbHost, dbPort, dbName)

	db, err = sql.Open("mysql", dsn)
	if err != nil {
		fmt.Println("Database connection failed:", err)
		os.Exit(1)
	}

	if err = db.Ping(); err != nil {
		fmt.Println("Database ping failed:", err)
		os.Exit(1)
	}

	fmt.Println("Database connected successfully")
}

func hashPassword(p string) string {
	h := md5.Sum([]byte(p + "salt_resource_share"))
	return hex.EncodeToString(h[:])
}

func generateToken(uid int, role string) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": uid,
		"role":    role,
		"exp":     time.Now().Add(24 * time.Hour).Unix(),
	}).SignedString(jwtSecret)
}

func parseToken(s string) (int, string, error) {
	t, err := jwt.Parse(s, func(*jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})
	if err != nil || !t.Valid {
		return 0, "", fmt.Errorf("invalid token")
	}
	c := t.Claims.(jwt.MapClaims)
	return int(c["user_id"].(float64)), c["role"].(string), nil
}

func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next(w, r)
	}
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "OPTIONS" {
			next(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		uid, role, err := parseToken(strings.TrimPrefix(auth, "Bearer "))
		if err != nil {
			http.Error(w, `{"error":"invalid token"}`, 401)
			return
		}
		r.Header.Set("X-User-ID", strconv.Itoa(uid))
		r.Header.Set("X-User-Role", role)
		next(w, r)
	}
}

func adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-Role") != "admin" {
			http.Error(w, `{"error":"admin required"}`, 403)
			return
		}
		next(w, r)
	})
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		return
	}
	var req struct{ Username, Password string }
	json.NewDecoder(r.Body).Decode(&req)
	if len(req.Username) < 3 || len(req.Password) < 6 {
		http.Error(w, `{"error":"用户名至少3位，密码至少6位"}`, 400)
		return
	}
	_, err := db.Exec("INSERT INTO users (username,password) VALUES (?,?)", req.Username, hashPassword(req.Password))
	if err != nil {
		http.Error(w, `{"error":"用户名已存在"}`, 400)
		return
	}
	jsonResponse(w, map[string]string{"message": "注册成功"})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		return
	}
	var req struct{ Username, Password string }
	json.NewDecoder(r.Body).Decode(&req)
	var u User
	err := db.QueryRow("SELECT id,username,role FROM users WHERE username=? AND password=?",
		req.Username, hashPassword(req.Password)).Scan(&u.ID, &u.Username, &u.Role)
	if err != nil {
		http.Error(w, `{"error":"用户名或密码错误"}`, 401)
		return
	}
	token, _ := generateToken(u.ID, u.Role)
	jsonResponse(w, map[string]interface{}{"token": token, "user": u})
}

func handleUser(w http.ResponseWriter, r *http.Request) {
	uid, _ := strconv.Atoi(r.Header.Get("X-User-ID"))
	var u User
	db.QueryRow("SELECT id,username,role,created_at FROM users WHERE id=?", uid).
		Scan(&u.ID, &u.Username, &u.Role, &u.Created)
	jsonResponse(w, u)
}

func handleUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		rows, _ := db.Query("SELECT id,username,role,created_at FROM users ORDER BY id")
		defer rows.Close()
		var users []User
		for rows.Next() {
			var u User
			rows.Scan(&u.ID, &u.Username, &u.Role, &u.Created)
			users = append(users, u)
		}
		jsonResponse(w, users)
	} else if r.Method == "POST" {
		var req struct{ Username, Password, Role string }
		json.NewDecoder(r.Body).Decode(&req)
		if req.Role == "" {
			req.Role = "user"
		}
		db.Exec("INSERT INTO users (username,password,role) VALUES (?,?,?)",
			req.Username, hashPassword(req.Password), req.Role)
		jsonResponse(w, map[string]string{"message": "创建成功"})
	}
}

func handleUserOps(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/users/")
	if r.Method == "PUT" {
		var req struct{ Username, Password, Role string }
		json.NewDecoder(r.Body).Decode(&req)
		if req.Password != "" {
			db.Exec("UPDATE users SET username=?,password=?,role=? WHERE id=?",
				req.Username, hashPassword(req.Password), req.Role, id)
		} else {
			db.Exec("UPDATE users SET username=?,role=? WHERE id=?", req.Username, req.Role, id)
		}
		jsonResponse(w, map[string]string{"message": "更新成功"})
	} else if r.Method == "DELETE" {
		db.Exec("DELETE FROM users WHERE id=? AND id!=1", id)
		jsonResponse(w, map[string]string{"message": "删除成功"})
	}
}

func handleResources(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit := 16
	if l, _ := strconv.Atoi(r.URL.Query().Get("limit")); l > 0 {
		limit = l
	}
	offset := (page - 1) * limit
	cat, search := r.URL.Query().Get("category"), r.URL.Query().Get("search")

	query := `SELECT r.id,r.name,r.orig_name,r.size,r.category,r.description,r.file_type,
		COALESCE(u.username,''),r.downloads,r.created_at FROM resources r 
		LEFT JOIN users u ON r.uploader_id=u.id WHERE 1=1`
	countQ := "SELECT COUNT(*) FROM resources r WHERE 1=1"
	var args []interface{}

	if cat != "" && cat != "全部" {
		query += " AND r.category=?"
		countQ += " AND category=?"
		args = append(args, cat)
	}
	if search != "" {
		query += " AND (r.orig_name LIKE ? OR r.description LIKE ?)"
		countQ += " AND (orig_name LIKE ? OR description LIKE ?)"
		args = append(args, "%"+search+"%", "%"+search+"%")
	}

	var total int
	db.QueryRow(countQ, args...).Scan(&total)

	query += " ORDER BY r.id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, _ := db.Query(query, args...)
	defer rows.Close()

	var resources []map[string]interface{}
	for rows.Next() {
		var id, downloads int
		var name, origName, cat, desc, ft, uploader, created string
		var size int64
		rows.Scan(&id, &name, &origName, &size, &cat, &desc, &ft, &uploader, &downloads, &created)
		resources = append(resources, map[string]interface{}{
			"id": id, "name": name, "orig_name": origName, "size": size, "category": cat,
			"description": desc, "file_type": ft, "uploader": uploader, "downloads": downloads,
			"created": created, "preview": getPreviewType(ft),
		})
	}

	pages := (total + limit - 1) / limit
	if pages < 1 {
		pages = 1
	}
	jsonResponse(w, map[string]interface{}{
		"resources": resources, "total": total, "page": page, "pages": pages,
	})
}

func handleResourceOps(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/resources/")

	if r.Method == "GET" {
		var rid, downloads int
		var name, origName, cat, desc, ft, uploader, created, fp string
		var size int64
		err := db.QueryRow(`SELECT r.id,r.name,r.orig_name,r.size,r.category,r.description,
			r.file_type,COALESCE(u.username,''),r.downloads,r.created_at,r.file_path 
			FROM resources r LEFT JOIN users u ON r.uploader_id=u.id WHERE r.id=?`, id).
			Scan(&rid, &name, &origName, &size, &cat, &desc, &ft, &uploader, &downloads, &created, &fp)
		if err != nil {
			http.Error(w, `{"error":"资源不存在"}`, 404)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"id": rid, "name": name, "orig_name": origName, "size": size, "category": cat,
			"description": desc, "file_type": ft, "uploader": uploader, "downloads": downloads,
			"created": created, "preview": getPreviewType(ft),
		})
	} else if r.Method == "PUT" {
		var req struct{ Description string }
		json.NewDecoder(r.Body).Decode(&req)
		db.Exec("UPDATE resources SET description=? WHERE id=?", req.Description, id)
		jsonResponse(w, map[string]string{"message": "更新成功"})
	} else if r.Method == "DELETE" {
		var fp string
		db.QueryRow("SELECT file_path FROM resources WHERE id=?", id).Scan(&fp)
		if fp != "" {
			os.Remove(fp)
		}
		db.Exec("DELETE FROM resources WHERE id=?", id)
		jsonResponse(w, map[string]string{"message": "删除成功"})
	}
}

func handleUploadProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		return
	}

	uploadID := strings.TrimPrefix(r.URL.Path, "/api/upload/progress/")
	fmt.Println("Progress query for uploadID:", uploadID)
	
	if uploadID == "" {
		fmt.Println("uploadID is empty")
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"upload ID required"}`, 400)
		return
	}

	progressMutex.RLock()
	progress, exists := uploadProgress[uploadID]
	progressMutex.RUnlock()

	fmt.Println("Progress exists:", exists, "uploaded:", progress.Uploaded, "total:", progress.TotalSize)

	if !exists {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"upload not found"}`, 404)
		return
	}

	elapsed := time.Since(progress.StartTime).Seconds()
	var speed float64
	if elapsed > 0 {
		speed = float64(progress.Uploaded) / elapsed
	}

	progressPercent := 0.0
	if progress.TotalSize > 0 {
		progressPercent = float64(progress.Uploaded) / float64(progress.TotalSize) * 100
	}

	jsonResponse(w, map[string]interface{}{
		"upload_id":      uploadID,
		"total_size":     progress.TotalSize,
		"uploaded":       progress.Uploaded,
		"progress":       progressPercent,
		"speed":          speed,
		"status":         progress.Status,
		"file_name":      progress.FileName,
		"error_message":  progress.ErrorMessage,
		"elapsed_time":   elapsed,
	})
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error":"Method not allowed"}`, 405)
		return
	}

	fmt.Println("Upload request received from user:", r.Header.Get("X-User-ID"))
	uploadID := uuid.New().String()
	const maxUploadSize = 7 * 1024 * 1024 * 1024
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	err := r.ParseMultipartForm(100 << 20)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(err.Error(), "request body too large") {
			http.Error(w, `{"error":"文件大小超过7GB限制"}`, 400)
			return
		}
		http.Error(w, `{"error":"读取文件失败"}`, 400)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"读取文件失败"}`, 400)
		return
	}
	defer file.Close()

	progressMutex.Lock()
	uploadProgress[uploadID] = &UploadProgress{
		TotalSize: header.Size,
		Uploaded:  0,
		StartTime: time.Now(),
		FileName:  header.Filename,
		Status:    "uploading",
	}
	progressMutex.Unlock()

	ext := filepath.Ext(header.Filename)
	newName := uuid.New().String() + ext
	filePath := filepath.Join(uploadDir, newName)

	dst, err := os.Create(filePath)
	if err != nil {
		progressMutex.Lock()
		uploadProgress[uploadID].Status = "error"
		uploadProgress[uploadID].ErrorMessage = "创建文件失败"
		progressMutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"创建文件失败"}`, 500)
		return
	}
	defer dst.Close()

	progressReader := &ProgressReader{
		Reader: file,
		OnProgress: func(read int64) {
			progressMutex.Lock()
			if progress, exists := uploadProgress[uploadID]; exists {
				progress.Uploaded = read
			}
			progressMutex.Unlock()
		},
	}

	written, err := io.Copy(dst, progressReader)
	if err != nil {
		os.Remove(filePath)
		progressMutex.Lock()
		uploadProgress[uploadID].Status = "error"
		uploadProgress[uploadID].ErrorMessage = "保存文件失败"
		progressMutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"保存文件失败"}`, 500)
		return
	}

	uid, _ := strconv.Atoi(r.Header.Get("X-User-ID"))
	ft := getFileType(ext)
	cat := getCategoryFromFileType(ft)
	description := r.FormValue("description")

	res, err := db.Exec(`INSERT INTO resources (name,orig_name,size,category,description,
		file_path,file_type,uploader_id) VALUES (?,?,?,?,?,?,?,?)`,
		newName, header.Filename, written, cat, description, filePath, ft, uid)

	if err != nil {
		os.Remove(filePath)
		progressMutex.Lock()
		uploadProgress[uploadID].Status = "error"
		uploadProgress[uploadID].ErrorMessage = "数据库写入失败"
		progressMutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"数据库写入失败"}`, 500)
		return
	}

	progressMutex.Lock()
	uploadProgress[uploadID].Status = "completed"
	uploadProgress[uploadID].Uploaded = written
	progressMutex.Unlock()

	fmt.Println("Upload completed, uploadID:", uploadID, "file:", header.Filename, "size:", written)
	
	id, _ := res.LastInsertId()
	jsonResponse(w, map[string]interface{}{
		"id":        id,
		"category":  cat,
		"message":   "上传成功",
		"upload_id": uploadID,
	})

	go func() {
		time.Sleep(5 * time.Minute)
		progressMutex.Lock()
		delete(uploadProgress, uploadID)
		progressMutex.Unlock()
	}()
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/download/")
	var fp, origName string
	err := db.QueryRow("SELECT file_path,orig_name FROM resources WHERE id=?", id).Scan(&fp, &origName)
	if err != nil || fp == "" {
		http.Error(w, "Not found", 404)
		return
	}
	db.Exec("UPDATE resources SET downloads=downloads+1 WHERE id=?", id)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, origName))
	http.ServeFile(w, r, fp)
}

func handlePreview(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/preview/")
	var fp, ft string
	err := db.QueryRow("SELECT file_path,file_type FROM resources WHERE id=?", id).Scan(&fp, &ft)
	if err != nil || fp == "" {
		http.Error(w, "Not found", 404)
		return
	}
	switch ft {
	case "image", "video", "audio", "pdf":
		http.ServeFile(w, r, fp)
	case "text", "code":
		data, _ := os.ReadFile(fp)
		if len(data) > 50000 {
			data = data[:50000]
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(data)
	default:
		http.Error(w, "Preview not supported", 400)
	}
}

func handleCategories(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, []string{"全部", "图片", "视频", "音频", "文档", "压缩包", "软件",
		"代码", "电子书", "设计资源", "字体", "办公模板", "学习资料", "游戏", "其他"})
}

func handleAnnouncements(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		rows, _ := db.Query("SELECT id,title,content,created_at FROM announcements ORDER BY id DESC LIMIT 10")
		defer rows.Close()
		var anns []map[string]interface{}
		for rows.Next() {
			var id int
			var title, content, created string
			rows.Scan(&id, &title, &content, &created)
			anns = append(anns, map[string]interface{}{
				"id": id, "title": title, "content": content, "created": created,
			})
		}
		if anns == nil {
			anns = []map[string]interface{}{}
		}
		jsonResponse(w, anns)
	} else if r.Method == "POST" {
		var req struct{ Title, Content string }
		json.NewDecoder(r.Body).Decode(&req)
		db.Exec("INSERT INTO announcements (title,content) VALUES (?,?)", req.Title, req.Content)
		jsonResponse(w, map[string]string{"message": "发布成功"})
	}
}

func handleAnnouncementOps(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/announcements/")
	if r.Method == "DELETE" {
		db.Exec("DELETE FROM announcements WHERE id=?", id)
		jsonResponse(w, map[string]string{"message": "删除成功"})
	}
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	var files, users, downloads int
	var size int64
	db.QueryRow("SELECT COUNT(*),COALESCE(SUM(size),0),COALESCE(SUM(downloads),0) FROM resources").
		Scan(&files, &size, &downloads)
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&users)
	jsonResponse(w, map[string]interface{}{
		"files": files, "users": users, "downloads": downloads, "size": size,
	})
}

func getFileType(ext string) string {
	ext = strings.ToLower(ext)
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg", ".ico":
		return "image"
	case ".mp4", ".webm", ".mkv", ".avi", ".mov", ".flv", ".wmv":
		return "video"
	case ".mp3", ".wav", ".flac", ".ogg", ".aac", ".m4a":
		return "audio"
	case ".pdf":
		return "pdf"
	case ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".rtf", ".md":
		return "document"
	case ".zip", ".rar", ".7z", ".tar", ".gz":
		return "archive"
	case ".exe", ".msi", ".dmg", ".pkg", ".deb", ".apk":
		return "software"
	case ".go", ".py", ".js", ".java", ".c", ".cpp", ".h", ".cs", ".php", ".rb",
		".html", ".css", ".json", ".xml", ".sql":
		return "code"
	case ".epub", ".mobi", ".azw":
		return "ebook"
	case ".psd", ".ai", ".sketch", ".fig":
		return "design"
	case ".ttf", ".otf", ".woff", ".woff2":
		return "font"
	default:
		return "other"
	}
}

func getCategoryFromFileType(ft string) string {
	m := map[string]string{
		"image": "图片", "video": "视频", "audio": "音频", "pdf": "文档",
		"document": "文档", "archive": "压缩包", "software": "软件", "code": "代码",
		"ebook": "电子书", "design": "设计资源", "font": "字体",
	}
	if c, ok := m[ft]; ok {
		return c
	}
	return "其他"
}

func getPreviewType(ft string) string {
	switch ft {
	case "image", "video", "audio", "pdf", "text", "code":
		return ft
	default:
		return "none"
	}
}