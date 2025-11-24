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

var db *sql.DB
var jwtSecret = []byte("your-secret-key-change-in-production")
var uploadDir = "./uploads"
var chunkDir = "./chunks"
var concurrentChunks = 1  // 改为1块

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

type UploadTask struct {
	ID          string `json:"id"`
	UserID      int    `json:"user_id"`
	FileName    string `json:"file_name"`
	FileSize    int64  `json:"file_size"`
	ChunkSize   int64  `json:"chunk_size"`
	TotalChunks int    `json:"total_chunks"`
	Uploaded    []int  `json:"uploaded"`
	Status      string `json:"status"`
	Progress    int    `json:"progress"`
	Description string `json:"description"`
	FilePath    string `json:"file_path"`
	Category    string `json:"category"`
	ResourceID  int64  `json:"resource_id"`
	Error       string `json:"error"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type TaskManager struct {
	sync.RWMutex
	tasks map[string]*UploadTask
}

var taskManager = &TaskManager{tasks: make(map[string]*UploadTask)}

func (tm *TaskManager) Get(id string) *UploadTask {
	tm.RLock()
	defer tm.RUnlock()
	return tm.tasks[id]
}

func (tm *TaskManager) Set(task *UploadTask) {
	tm.Lock()
	defer tm.Unlock()
	tm.tasks[task.ID] = task
	uploadedJSON, _ := json.Marshal(task.Uploaded)
	_, err := db.Exec(`INSERT INTO upload_tasks (id,user_id,file_name,file_size,chunk_size,total_chunks,uploaded,status,progress,description,file_path,category,resource_id,error,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE uploaded=?,status=?,progress=?,file_path=?,category=?,resource_id=?,error=?,updated_at=?`,
		task.ID, task.UserID, task.FileName, task.FileSize, task.ChunkSize, task.TotalChunks,
		string(uploadedJSON), task.Status, task.Progress, task.Description, task.FilePath, task.Category, task.ResourceID, task.Error, task.CreatedAt, task.UpdatedAt,
		string(uploadedJSON), task.Status, task.Progress, task.FilePath, task.Category, task.ResourceID, task.Error, task.UpdatedAt)
	if err != nil {
		// 日志输出已移除
	}
}

func (tm *TaskManager) Delete(id string) {
	tm.Lock()
	defer tm.Unlock()
	delete(tm.tasks, id)
	db.Exec("DELETE FROM upload_tasks WHERE id=?", id)
}

func (tm *TaskManager) GetUserTasks(userID int) []*UploadTask {
	tm.RLock()
	defer tm.RUnlock()
	var result []*UploadTask
	for _, t := range tm.tasks {
		if t.UserID == userID {
			result = append(result, t)
		}
	}
	return result
}

func (tm *TaskManager) loadTasks() {
	rows, err := db.Query(`SELECT id,user_id,file_name,file_size,chunk_size,total_chunks,uploaded,status,progress,description,file_path,category,resource_id,error,created_at,updated_at 
		FROM upload_tasks WHERE status NOT IN ('completed','failed') OR updated_at > ?`, time.Now().Add(-24*time.Hour).Unix())
	if err != nil {
		return
	}
	defer rows.Close()
	
	for rows.Next() {
		var t UploadTask
		var uploadedJSON string
		err := rows.Scan(&t.ID, &t.UserID, &t.FileName, &t.FileSize, &t.ChunkSize, &t.TotalChunks, &uploadedJSON, &t.Status, &t.Progress, &t.Description, &t.FilePath, &t.Category, &t.ResourceID, &t.Error, &t.CreatedAt, &t.UpdatedAt)
		if err != nil {
			continue
		}
		json.Unmarshal([]byte(uploadedJSON), &t.Uploaded)
		tm.tasks[t.ID] = &t
		
		if t.Status == "merging" || t.Status == "processing" {
			go tm.processTask(&t)
		}
	}
}

func (tm *TaskManager) processTask(task *UploadTask) {
	if task.Status == "merging" {
		tm.mergeChunks(task)
	} else {
		tm.finalizeFile(task)
	}
}

func (tm *TaskManager) mergeChunks(task *UploadTask) {
	task.Status = "merging"
	task.UpdatedAt = time.Now().Unix()
	tm.Set(task)

	ext := filepath.Ext(task.FileName)
	newName := uuid.New().String() + ext
	filePath := filepath.Join(uploadDir, newName)

	dst, err := os.Create(filePath)
	if err != nil {
		task.Status, task.Error = "failed", "创建文件失败"
		tm.Set(task)
		return
	}

	// 简化合并逻辑，因为只有1块
	chunkPath := filepath.Join(chunkDir, task.ID, "0")
	data, err := os.ReadFile(chunkPath)
	if err != nil {
		dst.Close()
		os.Remove(filePath)
		task.Status, task.Error = "failed", "读取分块失败"
		tm.Set(task)
		return
	}
	
	dst.Write(data)
	dst.Close()
	
	task.Progress = 75
	tm.Set(task)

	os.RemoveAll(filepath.Join(chunkDir, task.ID))

	task.FilePath = filePath
	task.Status = "processing"
	task.Progress = 75
	tm.Set(task)
	tm.finalizeFile(task)
}

func (tm *TaskManager) finalizeFile(task *UploadTask) {
	ext := filepath.Ext(task.FileName)
	fileType := getFileType(ext)
	task.Category = getCategoryFromFileType(fileType)

	info, _ := os.Stat(task.FilePath)
	task.Progress = 90
	tm.Set(task)

	res, err := db.Exec(`INSERT INTO resources (name,orig_name,size,category,description,file_path,file_type,uploader_id) VALUES (?,?,?,?,?,?,?,?)`,
		filepath.Base(task.FilePath), task.FileName, info.Size(), task.Category, task.Description, task.FilePath, fileType, task.UserID)
	if err != nil {
		os.Remove(task.FilePath)
		task.Status, task.Error = "failed", "数据库写入失败"
		tm.Set(task)
		return
	}

	task.ResourceID, _ = res.LastInsertId()
	task.Status, task.Progress = "completed", 100
	task.UpdatedAt = time.Now().Unix()
	tm.Set(task)
}

func handleUploadInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		FileName    string `json:"file_name"`
		FileSize    int64  `json:"file_size"`
		ChunkSize   int64  `json:"chunk_size"`
		Description string `json:"description"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	userID, _ := strconv.Atoi(r.Header.Get("X-User-ID"))
	
	// 强制设置为1块
	totalChunks := 1

	task := &UploadTask{
		ID: uuid.New().String(), UserID: userID, FileName: req.FileName, FileSize: req.FileSize,
		ChunkSize: req.FileSize, TotalChunks: totalChunks, Uploaded: []int{}, Status: "pending",  // ChunkSize 设为文件大小
		Description: req.Description, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}
	os.MkdirAll(filepath.Join(chunkDir, task.ID), 0755)
	taskManager.Set(task)
	
	jsonResponse(w, map[string]interface{}{"task_id": task.ID, "chunk_size": req.FileSize, "total_chunks": totalChunks})
}

func handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseMultipartForm(10 << 20)
	taskID := r.FormValue("task_id")
	chunkIdx, _ := strconv.Atoi(r.FormValue("chunk_index"))
	task := taskManager.Get(taskID)
	if task == nil {
		http.Error(w, `{"error":"任务不存在"}`, 404)
		return
	}
	userID, _ := strconv.Atoi(r.Header.Get("X-User-ID"))
	if task.UserID != userID {
		http.Error(w, `{"error":"无权操作"}`, 403)
		return
	}
	
	// 检查是否已上传
	if len(task.Uploaded) > 0 {
		jsonResponse(w, map[string]interface{}{"uploaded": len(task.Uploaded), "total": task.TotalChunks})
		return
	}
	
	file, _, err := r.FormFile("chunk")
	if err != nil {
		http.Error(w, `{"error":"读取分块失败"}`, 400)
		return
	}
	defer file.Close()
	dst, _ := os.Create(filepath.Join(chunkDir, taskID, "0"))  // 固定为0号分块
	written, _ := io.Copy(dst, file)
	dst.Close()

	task.Uploaded = []int{0}  // 只记录0号分块
	task.Status = "uploading"
	task.Progress = 100  // 上传完成直接100%
	task.UpdatedAt = time.Now().Unix()
	taskManager.Set(task)
	
	jsonResponse(w, map[string]interface{}{"uploaded": 1, "total": 1, "progress": 100})
}

func handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TaskID string `json:"task_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	task := taskManager.Get(req.TaskID)
	if task == nil {
		http.Error(w, `{"error":"任务不存在"}`, 404)
		return
	}
	
	// 检查是否已上传（只有1块）
	if len(task.Uploaded) != 1 {
		http.Error(w, `{"error":"分块不完整"}`, 400)
		return
	}
	
	jsonResponse(w, map[string]interface{}{"task_id": task.ID, "status": "merging"})
	go taskManager.mergeChunks(task)
}

func handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimPrefix(r.URL.Path, "/api/upload/status/")
	task := taskManager.Get(taskID)
	if task == nil {
		http.Error(w, `{"error":"任务不存在"}`, 404)
		return
	}
	jsonResponse(w, task)
}

func handleUploadTasks(w http.ResponseWriter, r *http.Request) {
	userID, _ := strconv.Atoi(r.Header.Get("X-User-ID"))
	tasks := taskManager.GetUserTasks(userID)
	if tasks == nil {
		tasks = []*UploadTask{}
	}
	jsonResponse(w, tasks)
}

func handleUploadCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != "DELETE" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := strings.TrimPrefix(r.URL.Path, "/api/upload/cancel/")
	task := taskManager.Get(taskID)
	if task == nil {
		jsonResponse(w, map[string]string{"message": "任务不存在"})
		return
	}
	os.RemoveAll(filepath.Join(chunkDir, taskID))
	if task.FilePath != "" {
		os.Remove(task.FilePath)
	}
	taskManager.Delete(taskID)
	jsonResponse(w, map[string]string{"message": "已取消"})
}

type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Created  string `json:"created"`
}

func main() {
	initDB()
	defer db.Close()
	os.MkdirAll(uploadDir, 0755)
	os.MkdirAll(chunkDir, 0755)
	taskManager.loadTasks()

	http.HandleFunc("/api/register", corsMiddleware(handleRegister))
	http.HandleFunc("/api/login", corsMiddleware(handleLogin))
	http.HandleFunc("/api/user", corsMiddleware(authMiddleware(handleUser)))
	http.HandleFunc("/api/users", corsMiddleware(adminMiddleware(handleUsers)))
	http.HandleFunc("/api/users/", corsMiddleware(adminMiddleware(handleUserOps)))
	http.HandleFunc("/api/resources", corsMiddleware(handleResources))
	http.HandleFunc("/api/resources/", corsMiddleware(handleResourceOps))
	http.HandleFunc("/api/upload", corsMiddleware(authMiddleware(handleUpload)))
	http.HandleFunc("/api/download/", corsMiddleware(handleDownload))
	http.HandleFunc("/api/preview/", corsMiddleware(handlePreview))
	http.HandleFunc("/api/categories", corsMiddleware(handleCategories))
	http.HandleFunc("/api/announcements", corsMiddleware(handleAnnouncements))
	http.HandleFunc("/api/announcements/", corsMiddleware(adminMiddleware(handleAnnouncementOps)))
	http.HandleFunc("/api/stats", corsMiddleware(handleStats))
	http.HandleFunc("/api/upload/init", corsMiddleware(authMiddleware(handleUploadInit)))
	http.HandleFunc("/api/upload/chunk", corsMiddleware(authMiddleware(handleUploadChunk)))
	http.HandleFunc("/api/upload/complete", corsMiddleware(authMiddleware(handleUploadComplete)))
	http.HandleFunc("/api/upload/status/", corsMiddleware(authMiddleware(handleUploadStatus)))
	http.HandleFunc("/api/upload/tasks", corsMiddleware(authMiddleware(handleUploadTasks)))
	http.HandleFunc("/api/upload/cancel/", corsMiddleware(authMiddleware(handleUploadCancel)))

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
		os.Exit(1)
	}
	
	db.Exec(`CREATE TABLE IF NOT EXISTS upload_tasks (
		id VARCHAR(64) PRIMARY KEY, user_id INT, file_name VARCHAR(255), file_size BIGINT, chunk_size BIGINT, total_chunks INT,
		uploaded TEXT, status VARCHAR(20) DEFAULT 'pending', progress INT DEFAULT 0, description TEXT, file_path VARCHAR(512),
		category VARCHAR(50), resource_id BIGINT, error TEXT, created_at BIGINT, updated_at BIGINT, INDEX(user_id), INDEX(status))`)
}

func hashPassword(p string) string {
	h := md5.Sum([]byte(p + "salt_resource_share"))
	return hex.EncodeToString(h[:])
}

func generateToken(uid int, role string) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": uid, "role": role, "exp": time.Now().Add(24 * time.Hour).Unix()}).SignedString(jwtSecret)
}

func parseToken(s string) (int, string, error) {
	t, err := jwt.Parse(s, func(*jwt.Token) (interface{}, error) { return jwtSecret, nil })
	if err != nil || !t.Valid {
		return 0, "", fmt.Errorf("invalid")
	}
	c := t.Claims.(jwt.MapClaims)
	return int(c["user_id"].(float64)), c["role"].(string), nil
}

func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
		if r.Method == "OPTIONS" {
			return
		}
		next(w, r)
	}
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
	err := db.QueryRow("SELECT id,username,role FROM users WHERE username=? AND password=?", req.Username, hashPassword(req.Password)).Scan(&u.ID, &u.Username, &u.Role)
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
	db.QueryRow("SELECT id,username,role,created_at FROM users WHERE id=?", uid).Scan(&u.ID, &u.Username, &u.Role, &u.Created)
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
		db.Exec("INSERT INTO users (username,password,role) VALUES (?,?,?)", req.Username, hashPassword(req.Password), req.Role)
		jsonResponse(w, map[string]string{"message": "创建成功"})
	}
}

func handleUserOps(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/users/")
	if r.Method == "PUT" {
		var req struct{ Username, Password, Role string }
		json.NewDecoder(r.Body).Decode(&req)
		if req.Password != "" {
			db.Exec("UPDATE users SET username=?,password=?,role=? WHERE id=?", req.Username, hashPassword(req.Password), req.Role, id)
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
	query := `SELECT r.id,r.name,r.orig_name,r.size,r.category,r.description,r.file_type,COALESCE(u.username,''),r.downloads,r.created_at FROM resources r LEFT JOIN users u ON r.uploader_id=u.id WHERE 1=1`
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
	jsonResponse(w, map[string]interface{}{"resources": resources, "total": total, "page": page, "pages": pages})
}

func handleResourceOps(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/resources/")
	if r.Method == "GET" {
		var rid, downloads int
		var name, origName, cat, desc, ft, uploader, created, fp string
		var size int64
		err := db.QueryRow(`SELECT r.id,r.name,r.orig_name,r.size,r.category,r.description,r.file_type,COALESCE(u.username,''),r.downloads,r.created_at,r.file_path FROM resources r LEFT JOIN users u ON r.uploader_id=u.id WHERE r.id=?`, id).Scan(&rid, &name, &origName, &size, &cat, &desc, &ft, &uploader, &downloads, &created, &fp)
		if err != nil {
			http.Error(w, `{"error":"资源不存在"}`, 404)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"id": rid, "name": name, "orig_name": origName, "size": size, "category": cat,
			"description": desc, "file_type": ft, "uploader": uploader, "downloads": downloads,
			"created": created, "preview": getPreviewType(ft),
		})
	}  else if r.Method == "PUT" {
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

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		return
	}
	r.ParseMultipartForm(32 << 20)
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, `{"error":"上传失败"}`, 400)
		return
	}
	defer file.Close()
	ext := filepath.Ext(header.Filename)
	newName := uuid.New().String() + ext
	filePath := filepath.Join(uploadDir, newName)
	dst, _ := os.Create(filePath)
	defer dst.Close()
	written, _ := io.Copy(dst, file)
	uid, _ := strconv.Atoi(r.Header.Get("X-User-ID"))
	ft := getFileType(ext)
	cat := getCategoryFromFileType(ft)
	res, _ := db.Exec(`INSERT INTO resources (name,orig_name,size,category,description,file_path,file_type,uploader_id) VALUES (?,?,?,?,?,?,?,?)`,
		newName, header.Filename, written, cat, r.FormValue("description"), filePath, ft, uid)
	id, _ := res.LastInsertId()
	jsonResponse(w, map[string]interface{}{"id": id, "category": cat})
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
	jsonResponse(w, []string{"全部", "图片", "视频", "音频", "文档", "压缩包", "软件", "代码", "电子书", "设计资源", "字体", "办公模板", "学习资料", "游戏", "其他"})
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
			anns = append(anns, map[string]interface{}{"id": id, "title": title, "content": content, "created": created})
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
	db.QueryRow("SELECT COUNT(*),COALESCE(SUM(size),0),COALESCE(SUM(downloads),0) FROM resources").Scan(&files, &size, &downloads)
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&users)
	jsonResponse(w, map[string]interface{}{"files": files, "users": users, "downloads": downloads, "size": size})
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
	case ".go", ".py", ".js", ".java", ".c", ".cpp", ".h", ".cs", ".php", ".rb", ".html", ".css", ".json", ".xml", ".sql":
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
	m := map[string]string{"image": "图片", "video": "视频", "audio": "音频", "pdf": "文档", "document": "文档", "archive": "压缩包", "software": "软件", "code": "代码", "ebook": "电子书", "design": "设计资源", "font": "字体"}
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
