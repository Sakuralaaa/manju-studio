package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const cookieName = "manju_session"

func signToken(exp int64) string {
	payload := "admin." + strconv.FormatInt(exp, 10)
	m := hmac.New(sha256.New, append(secret, []byte(adminPass)...)) // 改密码后旧会话失效
	m.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(m.Sum(nil))
}

func validSession(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || adminPass == "" {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return false
	}
	exp, _ := strconv.ParseInt(parts[1], 10, 64)
	if exp < time.Now().Unix() {
		return false
	}
	return hmac.Equal([]byte(signToken(exp)), []byte(c.Value))
}

func auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validSession(r) {
			httpErr(w, 401, "请先登录")
			return
		}
		h(w, r)
	})
}

var (
	failMu sync.Mutex
	fails  = map[string][]time.Time{}
)

func clientIP(r *http.Request) string {
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		return strings.TrimSpace(strings.Split(x, ",")[0])
	}
	h := r.RemoteAddr
	if i := strings.LastIndex(h, ":"); i > 0 {
		h = h[:i]
	}
	return h
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	_ = readJSON(r, &in)
	ip := clientIP(r)
	failMu.Lock()
	recent := []time.Time{}
	for _, t := range fails[ip] {
		if time.Since(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}
	fails[ip] = recent
	blocked := len(recent) >= 8
	failMu.Unlock()
	if blocked {
		httpErr(w, 429, "尝试次数过多，请稍后再试")
		return
	}
	if adminPass == "" {
		httpErr(w, 400, "服务端未设置管理员密码")
		return
	}
	if subtle.ConstantTimeCompare([]byte(in.Password), []byte(adminPass)) != 1 {
		failMu.Lock()
		fails[ip] = append(fails[ip], time.Now())
		failMu.Unlock()
		httpErr(w, 401, "密码不正确")
		return
	}
	exp := time.Now().Add(30 * 24 * time.Hour).Unix()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: signToken(exp), Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", Expires: time.Unix(exp, 0)})
	writeJSON(w, map[string]any{"ok": true})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]any{"ok": true})
}

func handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"authed": validSession(r), "version": version})
}

// ---------- 对外接口的固定密钥 ----------

func hashKey(k string) string {
	s := sha256.Sum256([]byte(k))
	return hex.EncodeToString(s[:])
}

func keyAuth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if k == "" {
			k = r.Header.Get("X-Api-Key")
		}
		var id int64
		if k != "" {
			_ = db.QueryRow(`SELECT id FROM api_keys WHERE key_hash=?`, hashKey(k)).Scan(&id)
		}
		if id == 0 {
			if validSession(r) { // 登录状态下也能直接调，方便在浏览器里试
				h(w, r)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="manju"`)
			httpErr(w, 401, "密钥无效")
			return
		}
		_, _ = db.Exec(`UPDATE api_keys SET last_used_at=? WHERE id=?`, now(), id)
		h(w, r)
	})
}

type APIKey struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Tail       string `json:"tail"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
}

func handleKeyList(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`SELECT id,name,tail,created_at,last_used_at FROM api_keys ORDER BY id DESC`)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		if rows.Scan(&k.ID, &k.Name, &k.Tail, &k.CreatedAt, &k.LastUsedAt) == nil {
			out = append(out, k)
		}
	}
	writeJSON(w, out)
}

func handleKeyCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	_ = readJSON(r, &in)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "未命名"
	}
	key := "mj-" + randHex(24)
	_, err := db.Exec(`INSERT INTO api_keys(name,key_hash,tail,created_at) VALUES(?,?,?,?)`, name, hashKey(key), key[len(key)-4:], now())
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	// 完整密钥只在创建时返回一次
	writeJSON(w, map[string]any{"key": key, "name": name})
}

func handleKeyDelete(w http.ResponseWriter, r *http.Request) {
	_, _ = db.Exec(`DELETE FROM api_keys WHERE id=?`, pathID(r))
	writeJSON(w, map[string]any{"ok": true})
}

var _ = fmt.Sprint
