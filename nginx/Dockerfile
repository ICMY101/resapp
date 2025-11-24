FROM nginx:alpine

# 复制自定义配置
COPY nginx.conf /etc/nginx/nginx.conf

# 创建必要的目录
RUN mkdir -p /app/web /app/uploads /app/chunks

# 暴露端口
EXPOSE 80

# 使用 Alpine 的 Nginx 默认启动命令
CMD ["nginx", "-g", "daemon off;"]