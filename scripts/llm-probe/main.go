// Command llm-probe 是一次性诊断工具：用目标实例的 AES 密钥解出模型网关凭据
// （明文只在进程内，绝不打印/落盘），再对上游做受控的 /v1/models 与
// /v1/chat/completions 请求，用于区分「模型 ID 无效」「凭据无效」「上游挂起」。
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/F31/ppts/internal/tenant"
	_ "modernc.org/sqlite"
)

func envOrConfig(key, configPath string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	f, err := os.Open(configPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+"="))
		}
	}
	return ""
}

func main() {
	dbPath := os.Getenv("PROBE_DB")
	configPath := os.Getenv("PROBE_CONFIG")
	name := os.Getenv("PROBE_GATEWAY")
	if name == "" {
		name = "local_llm"
	}
	if dbPath == "" {
		fmt.Println("需要 PROBE_DB")
		os.Exit(1)
	}

	keyB64 := envOrConfig("PPTS_GATEWAY_AES_KEY_BASE64", configPath)
	raw, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(raw) != 32 {
		fmt.Printf("AES key 不可用：decode=%v len=%d\n", err, len(raw))
		os.Exit(1)
	}
	cipher, err := tenant.NewAESGCMCipher(raw)
	if err != nil {
		fmt.Println("构造 cipher 失败:", err)
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Println("打开 DB 失败:", err)
		os.Exit(1)
	}
	defer db.Close()

	var tenantID, baseURL, model, kind string
	var enc []byte
	err = db.QueryRow(`SELECT tenant_id, base_url, model, kind, encrypted_creds
		FROM model_gateways WHERE name = ? AND kind = 'llm'`, name).
		Scan(&tenantID, &baseURL, &model, &kind, &enc)
	if err != nil {
		fmt.Println("读取网关行失败:", err)
		os.Exit(1)
	}
	aad := []byte("ppts:gateway:" + tenantID + ":" + name + ":" + kind)
	plain, err := cipher.Decrypt(enc, aad)
	if err != nil {
		fmt.Println("凭据解密失败:", err)
		os.Exit(1)
	}
	var creds struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(plain, &creds); err != nil || creds.APIKey == "" {
		fmt.Println("凭据内容异常:", err)
		os.Exit(1)
	}
	fp := sha256.Sum256([]byte(creds.APIKey))
	fmt.Printf("gateway=%s tenant=%s baseURL=%s model=%s keyLen=%d fingerprint=%x\n",
		name, tenantID, baseURL, model, len(creds.APIKey), fp[:4])
	key := creds.APIKey

	// 1) 带凭据列模型，确认凭据有效并拿到真实模型 ID。
	models, err := listModels(baseURL, key)
	if err != nil {
		fmt.Println("GET /v1/models 失败:", err)
	} else {
		fmt.Printf("models(%d)=%v\n", len(models), models)
	}

	// 2) 用库里配置的模型发最小 chat。
	chat(baseURL, key, model, "configured")
	// 3) 逐个测模型目录里的对话模型（跳过图像/视频），判断是否账户整体配额不足。
	for _, m := range models {
		if m == model || strings.Contains(m, "image") || strings.Contains(m, "video") {
			continue
		}
		chat(baseURL, key, m, "catalog")
	}
}

func do(baseURL, key, method, path string, body []byte, timeout time.Duration) (int, []byte, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, r)
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	started := time.Now()
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	elapsed := time.Since(started)
	if err != nil {
		return 0, nil, elapsed, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, out, elapsed, nil
}

func listModels(baseURL, key string) ([]string, error) {
	status, body, elapsed, err := do(baseURL, key, http.MethodGet, "/v1/models", nil, 20*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w (%.1fs)", err, elapsed.Seconds())
	}
	fmt.Printf("GET /v1/models -> %d (%.1fs) %s\n", status, elapsed.Seconds(), clip(string(body), 260))
	if status != http.StatusOK {
		return nil, fmt.Errorf("status %d", status)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		out = append(out, m.ID)
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out, nil
}

func chat(baseURL, key, model, label string) {
	payload, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	})
	status, body, elapsed, err := do(baseURL, key, http.MethodPost, "/v1/chat/completions", payload, 8*time.Second)
	if err != nil {
		fmt.Printf("POST chat[%s model=%s] -> ERR (%.1fs): %v\n", label, model, elapsed.Seconds(), err)
		return
	}
	fmt.Printf("POST chat[%s model=%s] -> %d (%.1fs) %s\n", label, model, status, elapsed.Seconds(), clip(string(body), 260))
}

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
