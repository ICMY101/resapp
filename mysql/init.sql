-- 创建数据库
CREATE DATABASE IF NOT EXISTS `resource_share` 
DEFAULT CHARACTER SET utf8mb4 
DEFAULT COLLATE utf8mb4_unicode_ci;

USE `resource_share`;

-- 先创建 users 表（被其他表引用）
CREATE TABLE IF NOT EXISTS `users` (
  `id` int NOT NULL AUTO_INCREMENT,
  `username` varchar(50) NOT NULL,
  `password` varchar(64) NOT NULL COMMENT 'MD5加盐哈希',
  `role` enum('user','admin') DEFAULT 'user',
  `created_at` timestamp NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `username` (`username`),
  KEY `idx_username` (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 插入默认管理员用户（密码: admin123）
INSERT IGNORE INTO `users` (`id`, `username`, `password`, `role`) VALUES
(1, 'admin', 'e99a18c428cb38d5f260853678922e03', 'admin');

-- 创建 announcements 表
CREATE TABLE IF NOT EXISTS `announcements` (
  `id` int NOT NULL AUTO_INCREMENT,
  `title` varchar(200) NOT NULL,
  `content` text,
  `is_active` tinyint(1) DEFAULT '1',
  `created_at` timestamp NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_active_created` (`is_active`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 插入示例公告
INSERT IGNORE INTO `announcements` (`title`, `content`) VALUES
('欢迎使用资源共享平台', '这是一个基于 Go + MySQL + Nginx 构建的文件分享系统，支持大文件分块上传和多种文件类型预览。');

-- 创建 resources 表（现在可以安全地引用 users 表）
CREATE TABLE IF NOT EXISTS `resources` (
  `id` int NOT NULL AUTO_INCREMENT,
  `name` varchar(255) NOT NULL COMMENT '存储的文件名',
  `orig_name` varchar(255) NOT NULL COMMENT '原始文件名',
  `size` bigint NOT NULL COMMENT '文件大小(字节)',
  `category` varchar(50) DEFAULT '其他' COMMENT '分类名称',
  `description` text COMMENT '资源描述',
  `file_path` varchar(500) NOT NULL COMMENT '文件存储路径',
  `file_type` varchar(50) DEFAULT NULL COMMENT '文件类型',
  `uploader_id` int DEFAULT NULL,
  `downloads` int DEFAULT '0' COMMENT '下载次数',
  `created_at` timestamp NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` timestamp NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `uploader_id` (`uploader_id`),
  KEY `idx_category` (`category`),
  KEY `idx_created` (`created_at`),
  KEY `idx_downloads` (`downloads`),
  FULLTEXT KEY `idx_search` (`orig_name`,`description`),
  CONSTRAINT `resources_ibfk_2` FOREIGN KEY (`uploader_id`) REFERENCES `users` (`id`) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 创建 upload_tasks 表
CREATE TABLE IF NOT EXISTS `upload_tasks` (
  `id` varchar(64) NOT NULL,
  `user_id` int DEFAULT NULL,
  `file_name` varchar(255) DEFAULT NULL,
  `file_size` bigint DEFAULT NULL,
  `chunk_size` bigint DEFAULT NULL,
  `total_chunks` int DEFAULT NULL,
  `uploaded` text,
  `status` varchar(20) DEFAULT 'pending',
  `progress` int DEFAULT '0',
  `description` text,
  `file_path` varchar(512) DEFAULT NULL,
  `category` varchar(50) DEFAULT NULL,
  `resource_id` bigint DEFAULT NULL,
  `error` text,
  `created_at` bigint DEFAULT NULL,
  `updated_at` bigint DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `user_id` (`user_id`),
  KEY `status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;