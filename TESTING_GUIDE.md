# 上传进度显示测试指南

## 问题描述
上传能成功（HTTP 200），但前端没有显示进度条、百分比、速度和时间。

## 修复内容
已添加全面的日志记录到以下函数，便于诊断问题：

### 前端修改 (web/index.html)
1. **startUpload** - 记录上传开始，显示全局进度面板
2. **uploadFileWithProgress** - 记录文件上传请求和响应
3. **checkUploadProgress** - 每秒轮询进度，记录所有响应
4. **updateProgressList** - 渲染进度条，记录每个项目的更新
5. **showGlobalProgress** - 记录进度面板的显示

### 后端修改 (go-app/main.go)
1. **handleUpload** - 记录上传完成时的信息
2. **handleUploadProgress** - 记录进度查询请求

## 测试步骤

### 1. 启动后端
```powershell
cd d:\Application\resApp\go-app
go build -o app.exe main.go
.\app.exe
```

后端会输出日志，例如：
```
Progress query for uploadID: abc123def456
Progress exists: true uploaded: 524288 total: 5242880
Upload completed, uploadID: abc123def456 file: test.txt size: 5242880
```

### 2. 打开浏览器控制台
1. 打开浏览器开发者工具 (F12)
2. 转到 Console 选项卡
3. 打开应用页面 http://localhost:8080 或 http://127.0.0.1:8080

### 3. 登录
1. 如果尚未登录，点击"🔐 登录"
2. 使用测试账号登录（例如 admin/admin）

### 4. 上传文件
1. 点击"📤 上传"按钮
2. 选择一个测试文件（推荐选择 > 1MB 的文件，这样能看到进度条更新）
3. 点击"📤 开始上传"
4. 观察浏览器控制台

## 预期日志输出

### 正常流程的日志
```
=== startUpload called ===
selectedFiles长度: 1
准备上传文件: 1 个
已关闭上传模态框
=== showGlobalProgress 被调用 ===
已添加 active 类，元素现在应该可见
=== uploadFileWithProgress called ===
文件名: test.txt
发送请求到: /api/upload
响应状态: 200
上传成功: {upload_id: "abc123...", ...}
获得到 upload_id: abc123...
设置初始进度信息: {fileName: "test.txt", ...}
activeUploads 现在包含: ["abc123..."]
已调用 updateProgressList()
=== updateProgressList called ===
activeUploads.size: 1
activeUploads 内容: [{id: "abc123...", name: "test.txt", progress: 0}]
渲染进度项 #1: abc123... 进度: 0% 速度: 0
=== checkUploadProgress 启动 ===
uploadId: abc123..., fileName: test.txt, fileSize: 1048576
[HH:MM:SS] 正在查询进度...
[HH:MM:SS] 响应状态: 200
[HH:MM:SS] 收到进度响应: {uploaded: 262144, progress: 25, speed: 10485760, ...}
[HH:MM:SS] 更新上传信息: {progress: 25, ...}
[HH:MM:SS] UI已更新,当前进度: 25.0%
[HH:MM:SS] 正在查询进度...
...（每秒重复）
```

## 可能的问题和解决方案

### 1. globalProgress 元素不存在
**症状**: 控制台显示 "无法找到 globalProgress 元素！"
**解决**: 检查 index.html 中是否有 `<div class="global-progress" id="globalProgress">` 元素

### 2. 收到 404 错误
**症状**: 控制台显示 `[HH:MM:SS] 收到404，上传可能还未完成初始化`
**解决**: 这是正常的，系统会继续轮询直到后端记录上传

### 3. 进度不更新
**症状**: 首次显示后，进度百分比不变
**可能原因**:
- 后端 handleUploadProgress 没有被调用
- 响应的 progress 字段为 0
- updateProgressList 没有更新 DOM

**调试步骤**:
1. 检查后端日志是否有 "Progress query for uploadID" 输出
2. 在浏览器开发者工具中检查网络请求，验证 `/api/upload/progress/...` 响应
3. 确认 updateProgressList 中的 `progressList.innerHTML` 被设置了

### 4. 上传完成后没有隐藏进度面板
**症状**: 上传完成后，进度面板仍然显示
**检查**: 确认 hideGlobalProgress() 函数被调用（查看日志）

## 关键调试变量

在浏览器控制台，可以检查这些变量：
```javascript
// 查看当前活跃的上传
console.log('activeUploads:', activeUploads)

// 查看进度面板元素
console.log(document.getElementById('globalProgress'))

// 查看进度列表
console.log(document.getElementById('progressList').innerHTML)

// 查看令牌
console.log('token:', token)

// 查看用户信息
console.log('user:', user)
```

## 后续步骤

如果问题仍未解决：
1. 收集完整的控制台日志
2. 检查浏览器网络面板（Network tab），确认：
   - POST /api/upload 返回 200 和正确的 upload_id
   - GET /api/upload/progress/{id} 返回 200 和递增的 progress 值
3. 检查后端终端日志
4. 如需要，在后端添加更多调试输出

## 性能说明
- 当前轮询间隔: 1000ms (1秒)，这是用户要求的更新频率
- 可以通过修改 setInterval 的参数来调整轮询频率
