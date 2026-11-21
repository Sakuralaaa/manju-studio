package main

// 通用临时上传（POST /v1/uploads）：给外部智能体传大音频 / 图片文件，避免在 MCP JSON-RPC 参数里塞几十 MB base64。
// 返回 upload_id（6 小时有效），可传给 upload_image / clone_voice / video_submit / upload_bgm / set_shot_sfx。

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	uploadMaxBytes = 80 << 20
	uploadTTL      = 6 * time.Hour
)

type uploadMeta struct {
	ID        string `json:"upload_id"`
	Filename  string `json:"filename"`
	Mime      string `json:"mime"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
}

func uploadDir() string { return filepath.Join(dataDir, "tmp", "uploads") }

func cleanUploads() {
	es, err := os.ReadDir(uploadDir())
	if err != nil {
		return
	}
	for _, e := range es {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > uploadTTL {
			_ = os.Remove(filepath.Join(uploadDir(), e.Name()))
		}
	}
}

func validUploadID(id string) bool {
	if !strings.HasPrefix(id, "up-") || len(id) < 10 || len(id) > 64 {
		return false
	}
	for _, r := range id[3:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func saveUploadBytes(data []byte, filename, contentType string) (uploadMeta, error) {
	if len(data) == 0 {
		return uploadMeta{}, errors.New("上传内容为空")
	}
	if len(data) > uploadMaxBytes {
		return uploadMeta{}, errors.New("上传文件超过 80 MB 限制")
	}
	cleanUploads()
	if err := os.MkdirAll(uploadDir(), 0o755); err != nil {
		return uploadMeta{}, err
	}
	id := "up-" + randHex(10)
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if mt, _, err := mime.ParseMediaType(contentType); err == nil && mt != "" {
		contentType = mt
	}
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "." || filename == "/" || filename == "" {
		filename = id
	}
	binPath := filepath.Join(uploadDir(), id+".bin")
	if err := os.WriteFile(binPath, data, 0o644); err != nil {
		return uploadMeta{}, err
	}
	meta := uploadMeta{ID: id, Filename: filename, Mime: contentType, Bytes: int64(len(data)), CreatedAt: now()}
	_ = os.WriteFile(filepath.Join(uploadDir(), id+".json"), []byte(mustJSON(meta)), 0o644)
	return meta, nil
}

// resolveUpload 校验 upload_id 并返回落盘路径与元数据
func resolveUpload(id string) (string, uploadMeta, error) {
	id = strings.TrimSpace(id)
	if !validUploadID(id) {
		return "", uploadMeta{}, errors.New("upload_id 无效")
	}
	binPath := filepath.Join(uploadDir(), id+".bin")
	fi, err := os.Stat(binPath)
	if err != nil || fi.IsDir() {
		return "", uploadMeta{}, errors.New("upload_id 不存在或已过期，请重新 POST /v1/uploads 上传")
	}
	if time.Since(fi.ModTime()) > uploadTTL {
		_ = os.Remove(binPath)
		_ = os.Remove(filepath.Join(uploadDir(), id+".json"))
		return "", uploadMeta{}, errors.New("upload_id 已过期，请重新 POST /v1/uploads 上传")
	}
	meta := uploadMeta{ID: id, Filename: id, Bytes: fi.Size()}
	if b, err := os.ReadFile(filepath.Join(uploadDir(), id+".json")); err == nil {
		_ = json.Unmarshal(b, &meta)
	}
	return binPath, meta, nil
}

func readUploadBytes(id string) ([]byte, uploadMeta, error) {
	p, meta, err := resolveUpload(id)
	if err != nil {
		return nil, uploadMeta{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, uploadMeta{}, errors.New("读取上传文件失败")
	}
	return data, meta, nil
}

// extUpload POST /v1/uploads：支持 multipart/form-data（字段 file）或直接二进制请求体（可选 X-Filename / Content-Type）
func extUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, uploadMaxBytes+(4<<20))
	ct := r.Header.Get("Content-Type")
	var data []byte
	var filename, fmime string
	var err error
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "multipart/form-data") {
		if err = r.ParseMultipartForm(uploadMaxBytes); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				httpErr(w, 413, "上传文件超过 80 MB 限制")
				return
			}
			httpErr(w, 400, "解析上传表单失败")
			return
		}
		f, fh, err2 := r.FormFile("file")
		if err2 != nil {
			httpErr(w, 400, "表单缺少 file 字段")
			return
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, uploadMaxBytes+1))
		if err != nil {
			httpErr(w, 400, "读取上传文件失败")
			return
		}
		if fh != nil {
			filename = fh.Filename
			fmime = fh.Header.Get("Content-Type")
		}
	} else {
		data, err = io.ReadAll(io.LimitReader(r.Body, uploadMaxBytes+1))
		if err != nil {
			httpErr(w, 400, "读取请求体失败")
			return
		}
		filename = r.Header.Get("X-Filename")
		if filename == "" {
			filename = r.URL.Query().Get("filename")
		}
		fmime = ct
	}
	if len(data) > uploadMaxBytes {
		httpErr(w, 413, "上传文件超过 80 MB 限制")
		return
	}
	meta, err := saveUploadBytes(data, filename, fmime)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok":             true,
		"upload_id":      meta.ID,
		"filename":       meta.Filename,
		"mime":           meta.Mime,
		"bytes":          meta.Bytes,
		"expires_in_sec": int(uploadTTL.Seconds()),
	})
}
