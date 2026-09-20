package handler

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pkg/sftp"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/sshx"
)

// maxUploadSize 单文件上传上限，避免把平台内存与磁盘打满
const maxUploadSize int64 = 512 << 20 // 512MB

// fileEntry 目录项
type fileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	IsDir   bool   `json:"isDir"`
	ModTime string `json:"modTime"`
}

// sftpSession 一次 SFTP 操作持有的连接，用完必须 Close
type sftpSession struct {
	conn   *sshx.Conn
	client *sftp.Client
}

func (s *sftpSession) Close() {
	if s.client != nil {
		_ = s.client.Close()
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

// openSFTP 建立到目标主机的 SFTP 会话，跳板机链路与终端复用同一套逻辑
func (h *Handler) openSFTP(host *model.Host) (*sftpSession, error) {
	conn, err := sshx.Dial(h.target(host), 20*time.Second)
	if err != nil {
		return nil, err
	}
	client, err := sftp.NewClient(conn.Client)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SFTP 子系统不可用: %w", err)
	}
	return &sftpSession{conn: conn, client: client}, nil
}

// cleanRemotePath 规范化远端路径，只接受绝对路径
func cleanRemotePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("路径必须是绝对路径")
	}
	return path.Clean(p), nil
}

// ListFiles 列出远端目录
func (h *Handler) ListFiles(c *gin.Context) {
	var host model.Host
	if err := h.DB.First(&host, idParam(c)).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return
	}
	dir, err := cleanRemotePath(c.Query("path"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	session, err := h.openSFTP(&host)
	if err != nil {
		response.Error(c, "连接失败: "+err.Error())
		return
	}
	defer session.Close()

	infos, err := session.client.ReadDir(dir)
	if err != nil {
		response.BadRequest(c, "读取目录失败: "+err.Error())
		return
	}

	entries := make([]fileEntry, 0, len(infos))
	for _, info := range infos {
		entries = append(entries, fileEntry{
			Name:    info.Name(),
			Path:    path.Join(dir, info.Name()),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			IsDir:   info.IsDir(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}

	response.OK(c, gin.H{"path": dir, "parent": path.Dir(dir), "entries": entries})
}

// DownloadFile 下载远端文件
func (h *Handler) DownloadFile(c *gin.Context) {
	var host model.Host
	if err := h.DB.First(&host, idParam(c)).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return
	}
	target, err := cleanRemotePath(c.Query("path"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	session, err := h.openSFTP(&host)
	if err != nil {
		h.recordFileAudit(c, &host, "download", target, "", 0, err)
		response.Error(c, "连接失败: "+err.Error())
		return
	}
	defer session.Close()

	info, err := session.client.Stat(target)
	if err != nil {
		h.recordFileAudit(c, &host, "download", target, "", 0, err)
		response.BadRequest(c, "文件不存在: "+err.Error())
		return
	}
	if info.IsDir() {
		response.BadRequest(c, "不支持直接下载目录，请先打包")
		return
	}

	remote, err := session.client.Open(target)
	if err != nil {
		h.recordFileAudit(c, &host, "download", target, "", 0, err)
		response.BadRequest(c, "打开文件失败: "+err.Error())
		return
	}
	defer remote.Close()

	filename := url.PathEscape(path.Base(target))
	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+filename)
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", fmt.Sprint(info.Size()))

	written, copyErr := io.Copy(c.Writer, remote)
	h.recordFileAudit(c, &host, "download", target, "", written, copyErr)
}

// UploadFile 上传文件到远端目录
func (h *Handler) UploadFile(c *gin.Context) {
	var host model.Host
	if err := h.DB.First(&host, idParam(c)).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return
	}
	dir, err := cleanRemotePath(c.PostForm("path"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	header, err := c.FormFile("file")
	if err != nil {
		response.BadRequest(c, "未收到上传文件")
		return
	}
	if header.Size > maxUploadSize {
		response.BadRequest(c, fmt.Sprintf("文件超过上限 %d MB", maxUploadSize>>20))
		return
	}

	src, err := header.Open()
	if err != nil {
		response.Error(c, "读取上传文件失败")
		return
	}
	defer src.Close()

	session, err := h.openSFTP(&host)
	if err != nil {
		h.recordFileAudit(c, &host, "upload", path.Join(dir, path.Base(header.Filename)), "", header.Size, err)
		response.Error(c, "连接失败: "+err.Error())
		return
	}
	defer session.Close()

	remotePath := path.Join(dir, path.Base(header.Filename))
	dst, err := session.client.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		h.recordFileAudit(c, &host, "upload", remotePath, "", header.Size, err)
		response.BadRequest(c, "创建远端文件失败: "+err.Error())
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, src)
	h.recordFileAudit(c, &host, "upload", remotePath, "", written, err)
	if err != nil {
		response.Error(c, "写入远端文件失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"path": remotePath, "size": written})
}

type filePathReq struct {
	Path string `json:"path" binding:"required"`
}

// MakeDir 新建远端目录
func (h *Handler) MakeDir(c *gin.Context) {
	var host model.Host
	if err := h.DB.First(&host, idParam(c)).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return
	}
	var req filePathReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "目录路径不能为空")
		return
	}
	target, err := cleanRemotePath(req.Path)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	session, err := h.openSFTP(&host)
	if err != nil {
		h.recordFileAudit(c, &host, "mkdir", target, "", 0, err)
		response.Error(c, "连接失败: "+err.Error())
		return
	}
	defer session.Close()

	mkErr := session.client.Mkdir(target)
	h.recordFileAudit(c, &host, "mkdir", target, "", 0, mkErr)
	if mkErr != nil {
		response.BadRequest(c, "创建目录失败: "+mkErr.Error())
		return
	}
	response.OK(c, gin.H{"path": target})
}

type renameReq struct {
	From string `json:"from" binding:"required"`
	To   string `json:"to" binding:"required"`
}

// RenameFile 重命名或移动
func (h *Handler) RenameFile(c *gin.Context) {
	var host model.Host
	if err := h.DB.First(&host, idParam(c)).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return
	}
	var req renameReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "源路径与目标路径均为必填")
		return
	}
	from, err := cleanRemotePath(req.From)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	to, err := cleanRemotePath(req.To)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	session, err := h.openSFTP(&host)
	if err != nil {
		h.recordFileAudit(c, &host, "rename", from, to, 0, err)
		response.Error(c, "连接失败: "+err.Error())
		return
	}
	defer session.Close()

	rnErr := session.client.Rename(from, to)
	h.recordFileAudit(c, &host, "rename", from, to, 0, rnErr)
	if rnErr != nil {
		response.BadRequest(c, "重命名失败: "+rnErr.Error())
		return
	}
	response.OK(c, gin.H{"from": from, "to": to})
}

// DeleteFile 删除文件或空目录
func (h *Handler) DeleteFile(c *gin.Context) {
	var host model.Host
	if err := h.DB.First(&host, idParam(c)).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return
	}
	target, err := cleanRemotePath(c.Query("path"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if target == "/" {
		response.BadRequest(c, "拒绝删除根目录")
		return
	}

	session, err := h.openSFTP(&host)
	if err != nil {
		h.recordFileAudit(c, &host, "delete", target, "", 0, err)
		response.Error(c, "连接失败: "+err.Error())
		return
	}
	defer session.Close()

	info, err := session.client.Stat(target)
	if err != nil {
		response.BadRequest(c, "目标不存在: "+err.Error())
		return
	}

	var delErr error
	if info.IsDir() {
		// 只删空目录：递归删除风险过高，需要时在终端里显式执行
		delErr = session.client.RemoveDirectory(target)
	} else {
		delErr = session.client.Remove(target)
	}
	h.recordFileAudit(c, &host, "delete", target, "", info.Size(), delErr)
	if delErr != nil {
		msg := "删除失败: " + delErr.Error()
		if info.IsDir() {
			msg += "（仅支持删除空目录）"
		}
		response.BadRequest(c, msg)
		return
	}
	response.OK(c, nil)
}

// ListFileAudits 文件操作留痕
func (h *Handler) ListFileAudits(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.FileAudit{})
	if username := c.Query("username"); username != "" {
		q = q.Where("username LIKE ?", "%"+username+"%")
	}
	if action := c.Query("action"); action != "" {
		q = q.Where("action = ?", action)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询文件留痕失败")
		return
	}
	var list []model.FileAudit
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询文件留痕失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// recordFileAudit 写入文件操作留痕，失败不影响主流程
func (h *Handler) recordFileAudit(c *gin.Context, host *model.Host, action, p, target string, size int64, opErr error) {
	entry := model.FileAudit{
		HostID: host.ID, HostName: host.Name, Address: host.Address,
		Action: action, Path: p, TargetPath: target, Size: size,
		Status: "success", ClientIP: c.ClientIP(),
	}
	if user := middleware.CurrentUser(c); user != nil {
		entry.UserID = user.ID
		entry.Username = user.Username
	}
	if opErr != nil {
		entry.Status = "failed"
		entry.ErrorMsg = truncate(opErr.Error(), 240)
	}
	_ = h.DB.Create(&entry).Error
}
