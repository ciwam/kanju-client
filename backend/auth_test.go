package backend

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// TestVerifyAndMigratePasswordBcrypt 验证 bcrypt 路径的密码校验。
func TestVerifyAndMigratePasswordBcrypt(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.db.Exec(
		`INSERT INTO app_user (username, password_hash, salt, role, token, token_expires_at, created_at)
		 VALUES ('bcrypt_user', ?, '', 'user', NULL, NULL, '2026-01-01T00:00:00+08:00')`,
		hashPassword("secret123")); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	rows, err := s.db.Query("SELECT id, username, password_hash, salt, role, token, token_expires_at, created_at FROM app_user WHERE username = 'bcrypt_user'")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("user not found")
	}
	u, err := scanUser(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !s.verifyAndMigratePassword(u, "secret123") {
		t.Fatal("正确密码应通过校验")
	}
	if s.verifyAndMigratePassword(u, "wrong") {
		t.Fatal("错误密码不应通过校验")
	}
}

// TestVerifyAndMigratePasswordLegacy 验证旧版 SHA-256+salt 账号在首次
// 正确登录后被静默迁移为 bcrypt（salt 清空）。
func TestVerifyAndMigratePasswordLegacy(t *testing.T) {
	s := newTestServer(t)
	salt := "legacysalt123"
	h := sha256.New()
	h.Write([]byte(salt))
	h.Write([]byte("oldpass"))
	legacyHash := base64.StdEncoding.EncodeToString(h.Sum(nil))
	if _, err := s.db.Exec(
		`INSERT INTO app_user (username, password_hash, salt, role, token, token_expires_at, created_at)
		 VALUES ('legacy_user', ?, ?, 'user', NULL, NULL, '2026-01-01T00:00:00+08:00')`,
		legacyHash, salt); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	rows, err := s.db.Query("SELECT id, username, password_hash, salt, role, token, token_expires_at, created_at FROM app_user WHERE username = 'legacy_user'")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("user not found")
	}
	u, err := scanUser(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !s.verifyAndMigratePassword(u, "oldpass") {
		t.Fatal("旧版正确密码应通过校验并迁移")
	}
	var saltNow, hashNow string
	if err := s.db.QueryRow("SELECT salt, password_hash FROM app_user WHERE username = 'legacy_user'").Scan(&saltNow, &hashNow); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if saltNow != "" {
		t.Fatalf("迁移后 salt 应为空，实际 %q", saltNow)
	}
	if hashNow == legacyHash {
		t.Fatal("迁移后哈希应为 bcrypt，不再是旧版哈希")
	}
}

// TestRegisterPasswordPolicy 验证注册时的密码长度约束（4-64 字符）。
func TestRegisterPasswordPolicy(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.registerUser("shortpw", "123"); err == nil {
		t.Fatal("3 字符密码应被拒绝")
	}
	if _, err := s.registerUser("okuser", "abcd"); err != nil {
		t.Fatalf("4 字符密码应通过: %v", err)
	}
	if _, err := s.registerUser("longpw", "abcdefghijklmnopqrstuvwxyz1234567890abcdefghijklmnopqrstuvwxyz123456789"); err == nil {
		t.Fatal("65 字符密码应被拒绝")
	}
}
