package backend

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// setAuthCookie 将认证 token 写入 httpOnly Cookie，防范 XSS 窃取。
// Secure 标志取决于实际传输通道是否加密：
//   - 服务直接以 TLS 提供服务（r.TLS != nil）；
//   - 或经反向代理 TLS 终止后转发（X-Forwarded-Proto: https）。
//
// 注意：绝不能按主机名（如“非 localhost”）推断加密与否——纯 HTTP 部署
// （无 TLS 反代）下若错误设置 Secure，浏览器将拒绝在后续请求中携带该
// cookie，表现为登录后立即回到登录页、全站 API 401（线上事故 2026-08）。
func setAuthCookie(w http.ResponseWriter, token string, r *http.Request) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	maxAge := int(TokenExpiry.Seconds())
	http.SetCookie(w, &http.Cookie{
		Name:     "kanju_token",
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearAuthCookie 清除认证 Cookie（登出时调用）。
func clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "kanju_token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
}

// authBypass 为「调试模式」开关（由 --no-auth 启动参数控制，默认关闭）。
// 开启时所有 API 接口跳过鉴权校验，无需登录即可直接访问，仅用于本地接口调试。
var authBypass = false

var usernameRE = regexp.MustCompile(`^[a-zA-Z0-9_\x{4e00}-\x{9fff}]+$`)

// hashPassword 使用 bcrypt 生成密码哈希（salt 由 bcrypt 内部嵌入）。
func hashPassword(password string) string {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		logError("hashPassword bcrypt 失败: %v", err)
		return ""
	}
	return string(b)
}

// hashPasswordLegacy 计算旧版 SHA-256+salt 哈希，仅用于迁移存量账号。
func hashPasswordLegacy(password, salt string) string {
	h := sha256.New()
	h.Write([]byte(salt))
	h.Write([]byte(password))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// verifyAndMigratePassword 验证密码是否正确，支持 bcrypt（当前）和 SHA-256+salt（旧版）
// 两种算法。对旧版账号在首次成功登录时自动静默迁移至 bcrypt，无需用户感知。
func (s *Server) verifyAndMigratePassword(u *User, password string) bool {
	// 当前路径：bcrypt 哈希（以 "$2" 开头）
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err == nil {
		return true
	}
	// 旧版路径：SHA-256+salt（首次登录时静默迁移至 bcrypt）
	if u.Salt != "" && hashPasswordLegacy(password, u.Salt) == u.PasswordHash {
		if newHash := hashPassword(password); newHash != "" {
			_, _ = s.db.Exec(
				"UPDATE app_user SET password_hash = ?, salt = '' WHERE id = ?",
				newHash, u.ID)
		}
		return true
	}
	return false
}

func scanUser(rows *sql.Rows) (*User, error) {
	var u User
	var token sql.NullString
	var expires sql.NullTime
	if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Salt, &u.Role, &token, &expires, &u.CreatedAt); err != nil {
		return nil, err
	}
	u.Token = token
	u.TokenExpiresAt = expires
	return &u, nil
}

func (s *Server) registerUser(username, password string) (*User, error) {
	// 长度按字符计（多字节中文安全）：下限 2 与前端一致，上限 20 防止
	// 超长用户名刷库/破坏 UI 布局（前端 maxLength=20 可被绕过，服务端强制）。
	uLen := utf8.RuneCountInString(username)
	if uLen < 2 {
		return nil, fmt.Errorf("用户名至少2个字符")
	}
	if uLen > 20 {
		return nil, fmt.Errorf("用户名最多20个字符")
	}
	if !usernameRE.MatchString(username) {
		return nil, fmt.Errorf("用户名只能包含字母、数字、下划线或中文")
	}
	pwLen := utf8.RuneCountInString(password)
	if pwLen < 4 {
		return nil, fmt.Errorf("密码至少4个字符")
	}
	// 上限提高至 64 字符（bcrypt 内部限制 72 字节，64 字符覆盖多字节编码的安全范围）
	if pwLen > 64 {
		return nil, fmt.Errorf("密码最多64个字符")
	}
	var cnt int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM app_user WHERE username = ?", username).Scan(&cnt)
	if cnt > 0 {
		return nil, fmt.Errorf("用户名已存在")
	}
	hash := hashPassword(password) // bcrypt 自带 salt，不再需要外部 salt
	token := newUUID()
	res, err := s.db.Exec(
		`INSERT INTO app_user (username, password_hash, salt, role, token, token_expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		username, hash, "", "user", token, time.Now().Add(TokenExpiry).Format(time.RFC3339), time.Now().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	u := &User{ID: id, Username: username, Role: "user", Token: sql.NullString{String: token, Valid: true}}
	s.tokens.put(token, tokenEntry{userID: id, role: "user", expiresAt: time.Now().Add(TokenExpiry)})
	return u, nil
}

func (s *Server) loginUser(username, password string) (*User, error) {
	if username == "" || password == "" {
		return nil, fmt.Errorf("用户名和密码不能为空")
	}
	rows, err := s.db.Query("SELECT id, username, password_hash, salt, role, token, token_expires_at, created_at FROM app_user WHERE username = ?", username)
	if err != nil {
		return nil, err
	}
	var u *User
	if rows.Next() {
		u, err = scanUser(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if u == nil {
		return nil, fmt.Errorf("用户名或密码错误")
	}
	if !s.verifyAndMigratePassword(u, password) {
		return nil, fmt.Errorf("用户名或密码错误")
	}
	newToken := newUUID()
	expiry := time.Now().Add(TokenExpiry)
	_, err = s.db.Exec("UPDATE app_user SET token = ?, token_expires_at = ? WHERE id = ?",
		newToken, expiry.Format(time.RFC3339), u.ID)
	if err != nil {
		return nil, err
	}
	s.tokens.put(newToken, tokenEntry{userID: u.ID, role: u.Role, expiresAt: expiry})
	u.Token = sql.NullString{String: newToken, Valid: true}
	u.TokenExpiresAt = sql.NullTime{Time: expiry, Valid: true}
	return u, nil
}

func (s *Server) userByToken(token string) (*User, error) {
	rows, err := s.db.Query("SELECT id, username, password_hash, salt, role, token, token_expires_at, created_at FROM app_user WHERE token = ?", token)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	return scanUser(rows)
}

func (s *Server) getUserByToken(token string) (*User, error) {
	if token == "" {
		return nil, nil
	}
	if e, ok := s.tokens.get(token); ok {
		return s.getUserByID(e.userID)
	}
	u, err := s.userByToken(token)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, nil
	}
	if u.TokenExpiresAt.Valid && time.Now().After(u.TokenExpiresAt.Time) {
		_, _ = s.db.Exec("UPDATE app_user SET token = NULL, token_expires_at = NULL WHERE id = ?", u.ID)
		return nil, nil
	}
	s.tokens.put(token, tokenEntry{userID: u.ID, role: u.Role, expiresAt: u.TokenExpiresAt.Time})
	return u, nil
}

func (s *Server) getUserByID(id int64) (*User, error) {
	rows, err := s.db.Query("SELECT id, username, password_hash, salt, role, token, token_expires_at, created_at FROM app_user WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		return scanUser(rows)
	}
	return nil, nil
}

func (s *Server) getAllUsers() ([]map[string]any, error) {
	rows, err := s.db.Query("SELECT id, username, role, created_at FROM app_user ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var username, role, createdAt string
		if err := rows.Scan(&id, &username, &role, &createdAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "username": username, "role": role, "createdAt": createdAt})
	}
	return out, nil
}

func (s *Server) logoutUser(token string) error {
	if token == "" {
		return nil
	}
	s.tokens.delete(token)
	_, err := s.db.Exec("UPDATE app_user SET token = NULL, token_expires_at = NULL WHERE token = ?", token)
	return err
}

func (s *Server) currentUser(r *http.Request) *User {
	if authBypass {
		// 调试模式（--no-auth）：绕过鉴权，返回一个具备管理员权限的虚拟用户，
		// 使包括管理接口在内的全部 API 均可直接访问，方便接口调试。
		return &User{ID: 0, Username: "debug", Role: "admin"}
	}
	u, _ := s.getUserByToken(bearerToken(r))
	return u
}

func (s *Server) requireUser(r *http.Request) (*User, error) {
	u := s.currentUser(r)
	if u == nil {
		return nil, fmt.Errorf("未登录")
	}
	return u, nil
}

func (s *Server) requireAdmin(r *http.Request) (*User, error) {
	u, err := s.requireUser(r)
	if err != nil {
		return nil, err
	}
	if u.Role != "admin" {
		return nil, fmt.Errorf("无权限")
	}
	return u, nil
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	// 注册开关：关闭时拒绝一切新用户注册（服务端强制，前端隐藏入口仅为体验优化）。
	if !s.isRegistrationOpen() {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": "当前已关闭新用户注册"})
		return
	}
	ip := clientIP(r)
	if s.rl.limited("register:"+ip, 5, 300) {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": "注册尝试过于频繁，请5分钟后再试"})
		return
	}
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": "请求格式错误"})
		return
	}
	u, err := s.registerUser(getStr(m, "username"), getStr(m, "password"))
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	setAuthCookie(w, u.Token.String, r)
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": map[string]any{
		"id": u.ID, "username": u.Username, "role": u.Role,
	}})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.rl.limited("login:"+ip, 10, 300) {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": "登录尝试过于频繁，请5分钟后再试"})
		return
	}
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": "请求格式错误"})
		return
	}
	u, err := s.loginUser(getStr(m, "username"), getStr(m, "password"))
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	setAuthCookie(w, u.Token.String, r)
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": map[string]any{
		"id": u.ID, "username": u.Username, "role": u.Role,
	}})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	_ = s.logoutUser(bearerToken(r))
	clearAuthCookie(w)
	writeJSON(w, 200, map[string]any{"isSuccess": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, err := s.requireUser(r)
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": map[string]any{
		"id": u.ID, "username": u.Username, "role": u.Role,
	}})
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireAdmin(r); err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	users, err := s.getAllUsers()
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": users})
}

func (s *Server) handleAdminHistory(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireAdmin(r); err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	var uid *int64
	if v := r.URL.Query().Get("userId"); v != "" {
		if id, err := parseInt64(v); err == nil {
			uid = &id
		}
	}
	var hist []*History
	var err error
	if uid != nil {
		hist, err = s.getHistory(uid)
	} else {
		hist, err = s.getAllHistory()
	}
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": hist})
}

func (s *Server) handleAdminFavorites(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireAdmin(r); err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	var uid *int64
	if v := r.URL.Query().Get("userId"); v != "" {
		if id, err := parseInt64(v); err == nil {
			uid = &id
		}
	}
	var favs []*Favorite
	var err error
	if uid != nil {
		favs, err = s.getFavorites(uid)
	} else {
		favs, err = s.getAllFavorites()
	}
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": favs})
}

// ---- 系统设置（settings 表，键值对） ----

func (s *Server) getSetting(key string) string {
	var v string
	_ = s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	return v
}

func (s *Server) setSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

// isRegistrationOpen 返回是否允许新用户注册。设置项不存在或值非 "false" 时
// 一律视为开启，保证向后兼容（旧库无该设置项时注册保持可用）。
func (s *Server) isRegistrationOpen() bool {
	return s.getSetting("registration_open") != "false"
}

func (s *Server) handleGetRegistration(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": map[string]any{"open": s.isRegistrationOpen()}})
}

func (s *Server) handleSetRegistration(w http.ResponseWriter, r *http.Request) {
	if _, err := s.requireAdmin(r); err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": "请求格式错误"})
		return
	}
	open := getBool(m, "open")
	if err := s.setSetting("registration_open", boolToSetting(open)); err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": map[string]any{"open": open}})
}

// getDefaultPlayMode 返回系统级默认播放方式（online/progressive），设置项缺失或非法时回退 online。
// 作为用户未单独配置时的兜底默认值（由 seedSettings 写入）。
func (s *Server) getDefaultPlayMode() string {
	if s.getSetting("default_play_mode") == "progressive" {
		return "progressive"
	}
	return "online"
}

// ---- 用户级偏好（user_prefs 表，每个用户独立配置） ----

// getUserPref 读取某用户的偏好项；不存在时返回 def。
func (s *Server) getUserPref(userID int64, key, def string) string {
	var v string
	_ = s.db.QueryRow("SELECT value FROM user_prefs WHERE user_id = ? AND key = ?", userID, key).Scan(&v)
	if v == "" {
		return def
	}
	return v
}

// setUserPref 写入（upsert）某用户的偏好项。
func (s *Server) setUserPref(userID int64, key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO user_prefs (user_id, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`,
		userID, key, value)
	return err
}

// getUserDefaultPlayMode 返回指定用户的默认播放方式：优先用户级偏好，未设置则回退系统级默认。
func (s *Server) getUserDefaultPlayMode(userID int64) string {
	if v := s.getUserPref(userID, "default_play_mode", ""); v == "progressive" || v == "online" {
		return v
	}
	return s.getDefaultPlayMode()
}

func (s *Server) handleGetDefaultPlayMode(w http.ResponseWriter, r *http.Request) {
	u, err := s.requireUser(r) // 任意登录用户均可读取自己的偏好
	if err != nil {
		logInfo("[PlayMode][BACKEND] GET 未登录拒绝: %v", err)
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	mode := s.getUserDefaultPlayMode(u.ID)
	logInfo("[PlayMode][BACKEND] GET userID=%d 返回 mode=%s", u.ID, mode)
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": map[string]any{"mode": mode}})
}

func (s *Server) handleSetDefaultPlayMode(w http.ResponseWriter, r *http.Request) {
	u, err := s.requireUser(r) // 任意登录用户均可设置自己的偏好（不再限 admin）
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": "请求格式错误"})
		return
	}
	mode := "online"
	if raw, ok := m["mode"].(string); ok && raw == "progressive" {
		mode = "progressive"
	}
	if err := s.setUserPref(u.ID, "default_play_mode", mode); err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": map[string]any{"mode": mode}})
}

// ---- 播放完成后自动删除已下载选集（用户级偏好，默认关闭） ----
// 仅当默认播放方式为 progressive（边下边播）时生效：选集播放完毕时，
// 自动清理本地通过边下边播下载的该选集文件，不影响其他已缓存数据。

func (s *Server) getUserAutoDeletePlayed(userID int64) bool {
	return s.getUserPref(userID, "auto_delete_played", "off") == "on"
}

func (s *Server) handleGetAutoDeletePlayed(w http.ResponseWriter, r *http.Request) {
	u, err := s.requireUser(r) // 任意登录用户均可读取自己的偏好
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	enabled := s.getUserAutoDeletePlayed(u.ID)
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": map[string]any{"enabled": enabled}})
}

func (s *Server) handleSetAutoDeletePlayed(w http.ResponseWriter, r *http.Request) {
	u, err := s.requireUser(r) // 任意登录用户均可设置自己的偏好
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	m, err := readJSONMap(w, r)
	if err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": "请求格式错误"})
		return
	}
	enabled := false
	if raw, ok := m["enabled"].(bool); ok {
		enabled = raw
	}
	if err := s.setUserPref(u.ID, "auto_delete_played", map[bool]string{true: "on", false: "off"}[enabled]); err != nil {
		writeJSON(w, 200, map[string]any{"isSuccess": false, "errorMsg": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"isSuccess": true, "data": map[string]any{"enabled": enabled}})
}
