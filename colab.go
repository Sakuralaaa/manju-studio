package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ---------- Google OAuth（桌面客户端 + PKCE，回调 http://localhost） ----------

const (
	colabScope   = "https://www.googleapis.com/auth/colaboratory"
	colabAPI     = "https://colaboratory.googleapis.com/"
	googleAuth   = "https://accounts.google.com/o/oauth2/v2/auth"
	googleToken  = "https://oauth2.googleapis.com/token"
	oauthRedirct = "http://localhost"
)

type oauthClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	ProjectID    string `json:"project_id"`
}

func loadOAuthClient() *oauthClient {
	raw := getSetting("colab_client", "")
	if raw == "" {
		return nil
	}
	var c oauthClient
	if json.Unmarshal([]byte(raw), &c) != nil || c.ClientID == "" {
		return nil
	}
	return &c
}

var (
	tokMu     sync.Mutex
	tokAccess string
	tokExp    time.Time
)

var googleHTTP = &http.Client{Timeout: 60 * time.Second}

func colabAccessToken(ctx context.Context) (string, error) {
	tokMu.Lock()
	defer tokMu.Unlock()
	if tokAccess != "" && time.Until(tokExp) > 2*time.Minute {
		return tokAccess, nil
	}
	c := loadOAuthClient()
	rt := getSetting("colab_refresh_token", "")
	if c == nil || rt == "" {
		return "", errors.New("还没有授权 Colab")
	}
	form := url.Values{"client_id": {c.ClientID}, "client_secret": {c.ClientSecret}, "refresh_token": {rt}, "grant_type": {"refresh_token"}}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Desc        string `json:"error_description"`
	}
	if err := postForm(ctx, googleToken, form, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		if out.Error == "invalid_grant" {
			return "", errors.New("授权已失效，请重新授权")
		}
		return "", fmt.Errorf("刷新令牌失败：%s %s", out.Error, out.Desc)
	}
	tokAccess = out.AccessToken
	tokExp = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return tokAccess, nil
}

func postForm(ctx context.Context, u string, form url.Values, out any) error {
	req, _ := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := googleHTTP.Do(req)
	if err != nil {
		return errors.New("连接 Google 失败：" + shortErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return json.Unmarshal(raw, out)
}

// colabCall 调用 Colab v1beta 接口
func colabCall(ctx context.Context, method, path string, body any, out any) error {
	tok, err := colabAccessToken(ctx)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, colabAPI+path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := googleHTTP.Do(req)
	if err != nil {
		return errors.New("连接 Colab 失败：" + shortErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == 401 {
		tokMu.Lock()
		tokAccess = ""
		tokMu.Unlock()
	}
	if resp.StatusCode >= 300 {
		return &colabErr{Code: resp.StatusCode, Msg: googleMsg(raw)}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type colabErr struct {
	Code int
	Msg  string
}

func (e *colabErr) Error() string { return fmt.Sprintf("Colab 返回 %d：%s", e.Code, e.Msg) }

func googleMsg(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		m := e.Error.Message
		for _, d := range e.Error.Details {
			if d.Reason != "" {
				m += "（" + d.Reason + "）"
			}
		}
		return m
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

type RuntimeKey struct {
	Variant     string `json:"variant"`
	Accelerator string `json:"accelerator"`
	Shape       string `json:"shape"`
}

type RuntimeSpec struct {
	Key      RuntimeKey `json:"key"`
	Eligible bool       `json:"eligible"`
}

type ConnInfo struct {
	URL        string `json:"url"`
	Token      string `json:"token"`
	ExpireTime string `json:"expireTime"`
}

type Runtime struct {
	Name           string     `json:"name"`
	RuntimeSpec    RuntimeKey `json:"runtimeSpec"`
	ConnectionInfo *ConnInfo  `json:"connectionInfo"`
}

func colabSubscription(ctx context.Context) (string, error) {
	var s struct {
		Tier string `json:"tier"`
	}
	err := colabCall(ctx, "GET", "v1beta/subscription", nil, &s)
	return s.Tier, err
}

func colabSpecs(ctx context.Context) ([]RuntimeSpec, error) {
	var s struct {
		RuntimeSpecs []RuntimeSpec `json:"runtimeSpecs"`
	}
	err := colabCall(ctx, "GET", "v1beta/runtimespecs", nil, &s)
	return s.RuntimeSpecs, err
}

func colabRuntimes(ctx context.Context) ([]Runtime, error) {
	var s struct {
		Runtimes []Runtime `json:"runtimes"`
	}
	err := colabCall(ctx, "GET", "v1beta/runtimes", nil, &s)
	return s.Runtimes, err
}

func colabGetRuntime(ctx context.Context, name string) (*Runtime, error) {
	var rt Runtime
	if err := colabCall(ctx, "GET", "v1beta/"+name, nil, &rt); err != nil {
		return nil, err
	}
	return &rt, nil
}

func colabCreateRuntime(ctx context.Context, id string, key RuntimeKey) error {
	var op struct {
		Name  string `json:"name"`
		Done  bool   `json:"done"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := colabCall(ctx, "POST", "v1beta/runtimes?runtimeId="+url.QueryEscape(id), map[string]any{"runtimeSpec": key}, &op); err != nil {
		return err
	}
	if op.Error != nil {
		return fmt.Errorf("Colab 拒绝分配：%s", op.Error.Message)
	}
	return nil
}

func colabDeleteRuntime(ctx context.Context, name string) error {
	return colabCall(ctx, "DELETE", "v1beta/"+name+"?allowMissing=true", nil, nil)
}

// ---------- 设置页接口 ----------

var tierLabels = map[string]string{
	"SUBSCRIPTION_TIER_FREE": "免费版", "SUBSCRIPTION_TIER_PRO": "Pro", "SUBSCRIPTION_TIER_PRO_PLUS": "Pro+",
}

func handleColabClientUpload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		JSON string `json:"json"`
	}
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(in.JSON), &raw); err != nil {
		httpErr(w, 400, "不是有效的 JSON 文件")
		return
	}
	var c oauthClient
	for _, k := range []string{"installed", "web"} {
		if v, ok := raw[k]; ok {
			_ = json.Unmarshal(v, &c)
		}
	}
	if c.ClientID == "" {
		_ = json.Unmarshal([]byte(in.JSON), &c)
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		httpErr(w, 400, "文件里没有客户端编号和密钥")
		return
	}
	_ = setSetting("colab_client", mustJSON(c))
	delSetting("colab_refresh_token")
	delSetting("colab_scopes")
	tokMu.Lock()
	tokAccess = ""
	tokMu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

func handleColabClientDelete(w http.ResponseWriter, r *http.Request) {
	delSetting("colab_client")
	delSetting("colab_refresh_token")
	delSetting("colab_info")
	tokMu.Lock()
	tokAccess = ""
	tokMu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

func handleColabAuthStart(w http.ResponseWriter, r *http.Request) {
	c := loadOAuthClient()
	if c == nil {
		httpErr(w, 400, "请先上传客户端文件")
		return
	}
	verifier := randHex(32)
	state := randHex(8)
	_ = setSetting("colab_pkce", mustJSON(map[string]string{"verifier": verifier, "state": state}))
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{
		"client_id": {c.ClientID}, "redirect_uri": {oauthRedirct}, "response_type": {"code"}, "scope": {authScopes()},
		"access_type": {"offline"}, "prompt": {"consent"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state},
	}
	writeJSON(w, map[string]any{"url": googleAuth + "?" + q.Encode()})
}

func handleColabAuthFinish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	_ = readJSON(r, &in)
	c := loadOAuthClient()
	var pk struct {
		Verifier string `json:"verifier"`
		State    string `json:"state"`
	}
	_ = json.Unmarshal([]byte(getSetting("colab_pkce", "{}")), &pk)
	if c == nil || pk.Verifier == "" {
		httpErr(w, 400, "请先点「打开授权页」")
		return
	}
	raw := strings.TrimSpace(in.URL)
	code := raw
	if strings.Contains(raw, "code=") {
		u, err := url.Parse(raw)
		if err != nil {
			httpErr(w, 400, "地址格式不对")
			return
		}
		q := u.Query()
		if e := q.Get("error"); e != "" {
			httpErr(w, 400, "授权被拒绝："+e)
			return
		}
		if s := q.Get("state"); s != "" && s != pk.State {
			httpErr(w, 400, "这条地址不是本次授权生成的，请重新打开授权页")
			return
		}
		code = q.Get("code")
	}
	if code == "" {
		httpErr(w, 400, "地址里没有授权码")
		return
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthRedirct},
		"client_id": {c.ClientID}, "client_secret": {c.ClientSecret}, "code_verifier": {pk.Verifier}}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
		Desc         string `json:"error_description"`
	}
	if err := postForm(r.Context(), googleToken, form, &out); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if out.RefreshToken == "" {
		msg := out.Error
		if out.Desc != "" {
			msg += "：" + out.Desc
		}
		if msg == "" {
			msg = "没有拿到长期令牌"
		}
		httpErr(w, 400, "授权失败 "+msg)
		return
	}
	_ = setSetting("colab_refresh_token", out.RefreshToken)
	_ = setSetting("colab_scopes", orDefault(out.Scope, colabScope))
	delSetting("colab_pkce")
	tokMu.Lock()
	tokAccess, tokExp = out.AccessToken, time.Now().Add(time.Duration(out.ExpiresIn)*time.Second)
	tokMu.Unlock()
	_, _ = refreshColabInfo(r.Context())
	writeJSON(w, map[string]any{"ok": true})
}

type colabInfo struct {
	Tier      string        `json:"tier"`
	TierLabel string        `json:"tier_label"`
	Specs     []RuntimeSpec `json:"specs"`
	FetchedAt int64         `json:"fetched_at"`
	Error     string        `json:"error"`
}

func refreshColabInfo(ctx context.Context) (*colabInfo, error) {
	info := &colabInfo{FetchedAt: now()}
	tier, err := colabSubscription(ctx)
	if err != nil {
		info.Error = err.Error()
		_ = setSetting("colab_info", mustJSON(info))
		return info, err
	}
	info.Tier, info.TierLabel = tier, tierLabels[tier]
	if info.TierLabel == "" {
		info.TierLabel = "未知"
	}
	specs, err := colabSpecs(ctx)
	if err != nil {
		info.Error = err.Error()
	}
	info.Specs = specs
	_ = setSetting("colab_info", mustJSON(info))
	return info, err
}

func cachedColabInfo() *colabInfo {
	var info colabInfo
	if json.Unmarshal([]byte(getSetting("colab_info", "")), &info) != nil {
		return nil
	}
	return &info
}

func handleColabRefresh(w http.ResponseWriter, r *http.Request) {
	info, err := refreshColabInfo(r.Context())
	if err != nil && info.Tier == "" {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, info)
}

// authScopes 开启云端硬盘权重缓存时多申请 drive.file（只能访问本应用自己创建的文件）
func authScopes() string {
	if loadColabCfg().DriveCache {
		return colabScope + " " + driveScope
	}
	return colabScope
}
