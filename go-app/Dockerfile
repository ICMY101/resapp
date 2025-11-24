FROM golang:1.21-alpine

# 安装必要的工具
RUN apk add --no-cache git

WORKDIR /app

# 复制依赖文件
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码
COPY . .

# 构建应用
RUN go build -o main .

# 创建必要的目录
RUN mkdir -p /app/uploads /app/chunks

# 暴露端口
EXPOSE 8080

# 运行应用
CMD ["./main"]