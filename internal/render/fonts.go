package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/F31/ppts/internal/fonts"
)

// 这个文件让渲染服务多一个能力：**按字符集裁字体**，给 HTML 成品包内嵌用。
//
// 为什么这件事落在渲染服务，而不是主仓库或另起一个服务：
//
//	子集化需要 fontTools（CFF 轮廓裁剪没有可用的纯 Go 实现），而 fontTools 只有在
//	渲染镜像里才是现成的——镜像本来就带一份「全 URO 子集」的母版字体（charset.txt
//	的产物），再裁一次只要 python3-fonttools。另起一个 sidecar 意味着多一个部署单元、
//	多一套健康检查与告警，换来的是「渲染镜像不加 15MB」——这个交换不划算。
//
// 为什么是「二次子集」而不是直接读源 TTC：
//
//	源 TTC 是 19MB，母版是 11MB。二次子集少装 8MB，而且**天然继承 charset.txt 的边界**：
//	母版里没有的字（CJK Ext-A/B）在二次子集里同样没有，会被如实报成 missing，
//	而不是悄悄从源字体里补上——「覆盖范围的唯一事实源」因此仍然只有 charset.txt 一个。
//
// 边界：这个端点只接受码位列表，不接受字体文件、不接受路径。产物在临时目录里生成、
// 读完即删；缓存键是字集摘要，值是不可变的字节。

// FontSubsetConfig 是子集器的配置。BasePath 为空时服务不注册该端点（旧部署行为不变）。
type FontSubsetConfig struct {
	// BasePath 是母版 OTF（charset.txt 的产物，镜像内 /usr/share/fonts/opentype/ppts/）。
	BasePath string
	// ScriptPath 是运行期子集脚本（deploy/renderer/subset-runtime.py）。
	ScriptPath string
	// PythonPath 是解释器（空 = python3）。
	PythonPath string
	// Family 是母版字体族名，仅用于响应标识。
	Family string
	// Version 是母版版本（镜像构建期由 charset 摘要推导），参与缓存键与 planHash 无关。
	Version string
	// Timeout 是单次子集的超时（0 = 20s）。裁 200 字实测约 0.3s，20s 是给病态输入留的余量。
	Timeout time.Duration
	// MaxConcurrent 是同时跑的子集进程数（0 = 2）。fontTools 加载母版约 100–200MB 内存，
	// 与渲染任务抢同一块 mem_limit，所以默认给得很小。
	MaxConcurrent int
	// MaxCodepoints 是单次请求的码位上限（0 = 8000）。整本小说也到不了这个量，
	// 设上限是为了让「拿全 URO 来打」这种请求在入口就被拒，而不是跑满 20 秒再超时。
	MaxCodepoints int
}

// FontSubset 是一次子集的结果。
type FontSubset struct {
	Family      string
	Format      string
	BaseVersion string
	Data        []byte
	Codepoints  int
	// Missing 是请求了但母版里没有的码位：生僻字、CJK Ext-A/B、emoji。
	// 这不是错误——浏览器会沿 fallback 链找，但调用方必须知道哪些字不在包里，
	// 否则「字体子集覆盖率 100%」就成了自欺。
	Missing []rune
}

// subsetRunner 执行一次子集脚本，返回 stdout 里的 JSON 报告（产物写到 outPath）。
//
// 抽成接口是为了让「缓存、缺字校验、超时、并发上限」这些**本包自己的逻辑**能在
// 纯 Go 测试里被精确驱动：真跑 python 才能验证的东西只有进程调用本身，而那部分
// 该由一条环境依赖的集成测试盯着，不该让每个用例都依赖解释器存在。
type subsetRunner interface {
	run(ctx context.Context, charsetPath, outPath string) ([]byte, error)
}

// execRunner 用外部进程跑脚本。
type execRunner struct {
	python string
	script string
	base   string
}

func (r execRunner) run(ctx context.Context, charsetPath, outPath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.python, r.script,
		"--base", r.base,
		"--charset", charsetPath,
		"--out", outPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// 子进程不该继承服务的环境变量（里面可能有对象存储密钥）。
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "PYTHONDONTWRITEBYTECODE=1"}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if len(msg) > 800 {
			msg = msg[:800] + "…"
		}
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	return stdout.Bytes(), nil
}

// FontSubsetter 串行化地跑子集脚本，并把结果按字集摘要缓存。
type FontSubsetter struct {
	cfg    FontSubsetConfig
	runner subsetRunner
	sem    chan struct{}

	mu         sync.Mutex
	cache      map[string]*FontSubset
	order      []string
	cacheLimit int
}

// NewFontSubsetter 构造子集器。母版或脚本不存在时直接失败：
// 配错了应该在服务起不来的时候就报，而不是让每个导出请求各失败一次。
func NewFontSubsetter(cfg FontSubsetConfig) (*FontSubsetter, error) {
	if strings.TrimSpace(cfg.BasePath) == "" {
		return nil, errors.New("render: font base path is required")
	}
	if st, err := os.Stat(cfg.BasePath); err != nil || st.IsDir() {
		return nil, fmt.Errorf("render: font base %q: %w", cfg.BasePath, orErr(err, "not a file"))
	}
	if strings.TrimSpace(cfg.ScriptPath) == "" {
		return nil, errors.New("render: font subset script is required")
	}
	if st, err := os.Stat(cfg.ScriptPath); err != nil || st.IsDir() {
		return nil, fmt.Errorf("render: font subset script %q: %w", cfg.ScriptPath, orErr(err, "not a file"))
	}
	cfg = fontSubsetDefaults(cfg)
	return &FontSubsetter{
		cfg: cfg,
		runner: execRunner{
			python: cfg.PythonPath,
			script: cfg.ScriptPath,
			base:   cfg.BasePath,
		},
		sem:        make(chan struct{}, cfg.MaxConcurrent),
		cache:      make(map[string]*FontSubset, 8),
		cacheLimit: 8,
	}, nil
}

func fontSubsetDefaults(cfg FontSubsetConfig) FontSubsetConfig {
	if cfg.PythonPath == "" {
		cfg.PythonPath = "python3"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}
	if cfg.MaxCodepoints <= 0 {
		cfg.MaxCodepoints = 8000
	}
	if cfg.Family == "" {
		cfg.Family = "Noto Sans CJK SC"
	}
	return cfg
}

// newFontSubsetterWithRunner 给测试注入 runner，不检查母版与脚本是否存在。
func newFontSubsetterWithRunner(cfg FontSubsetConfig, runner subsetRunner) *FontSubsetter {
	cfg = fontSubsetDefaults(cfg)
	return &FontSubsetter{
		cfg:        cfg,
		runner:     runner,
		sem:        make(chan struct{}, cfg.MaxConcurrent),
		cache:      make(map[string]*FontSubset, 8),
		cacheLimit: 8,
	}
}

// MaxCodepoints 返回单次请求的码位上限。
func (s *FontSubsetter) MaxCodepoints() int { return s.cfg.MaxCodepoints }

// Subset 裁剪母版，返回只含 cps 的字体。
func (s *FontSubsetter) Subset(ctx context.Context, cps []rune) (*FontSubset, error) {
	want := normalizeCodepoints(cps)
	if len(want) == 0 {
		return nil, errors.New("render: font subset: no codepoints")
	}
	if len(want) > s.cfg.MaxCodepoints {
		return nil, fmt.Errorf("render: font subset: %d codepoints exceeds limit %d",
			len(want), s.cfg.MaxCodepoints)
	}
	key := s.cacheKey(want)
	if cached, ok := s.lookup(key); ok {
		return cached, nil
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	out, err := s.run(ctx, want)
	if err != nil {
		return nil, err
	}
	s.store(key, out)
	return out, nil
}

// run 启一次子进程。临时目录每次新建：并发请求不能共用一个工作目录。
func (s *FontSubsetter) run(ctx context.Context, want []rune) (*FontSubset, error) {
	dir, err := os.MkdirTemp("", "ppts-fontsubset-")
	if err != nil {
		return nil, fmt.Errorf("render: font subset: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	charsetPath := filepath.Join(dir, "charset.txt")
	outPath := filepath.Join(dir, "subset.otf")
	if err := os.WriteFile(charsetPath, []byte(formatCodepointList(want)), 0o600); err != nil {
		return nil, fmt.Errorf("render: font subset: write charset: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	stdout, err := s.runner.run(ctx, charsetPath, outPath)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("render: font subset: timed out after %s", s.cfg.Timeout)
		}
		return nil, fmt.Errorf("render: font subset: %w", err)
	}

	var report struct {
		Kept    []uint32 `json:"kept"`
		Missing []uint32 `json:"missing"`
	}
	if err := json.Unmarshal(stdout, &report); err != nil {
		return nil, fmt.Errorf("render: font subset: bad report: %w", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("render: font subset: read result: %w", err)
	}

	// 独立校验：不采信脚本自报的 kept 列表，直接读产物 cmap。
	// 门禁与实现用同一个账本时，账本错了两边一起错。
	cov, err := fonts.ReadFontCoverage(data)
	if err != nil {
		return nil, fmt.Errorf("render: font subset: parse result: %w", err)
	}
	kept := make([]rune, 0, len(report.Kept))
	for _, cp := range report.Kept {
		kept = append(kept, rune(cp))
	}
	if missing := cov.Missing(kept); len(missing) > 0 {
		return nil, fmt.Errorf("render: font subset: 产物缺 %d 个已声明码位（首个 %U）",
			len(missing), missing[0])
	}

	missingRunes := make([]rune, 0, len(report.Missing))
	for _, cp := range report.Missing {
		missingRunes = append(missingRunes, rune(cp))
	}
	return &FontSubset{
		Family:      s.cfg.Family,
		Format:      "otf",
		BaseVersion: s.cfg.Version,
		Data:        data,
		Codepoints:  len(kept),
		Missing:     missingRunes,
	}, nil
}

func (s *FontSubsetter) cacheKey(want []rune) string {
	h := sha256.New()
	h.Write([]byte(s.cfg.Version))
	h.Write([]byte{0})
	for _, r := range want {
		var b [4]byte
		b[0] = byte(r >> 24)
		b[1] = byte(r >> 16)
		b[2] = byte(r >> 8)
		b[3] = byte(r)
		h.Write(b[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *FontSubsetter) lookup(key string) (*FontSubset, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.cache[key]
	return v, ok
}

// store 做 FIFO 淘汰。缓存的是不可变字节，所以不需要考虑「过期」——
// 母版换版本时 Version 变了，键自然全部失配。
func (s *FontSubsetter) store(key string, v *FontSubset) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.cache[key]; exists {
		return
	}
	s.cache[key] = v
	s.order = append(s.order, key)
	for len(s.order) > s.cacheLimit {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.cache, oldest)
	}
}

// SubsetFont 请求渲染服务按字符集裁一份字体。
//
// 端点不存在（404，即该部署未启用字体子集）时返回 ErrFontSubsetUnavailable：
// 上层据此直接走「不内嵌字体」的降级，而不是把它当成一次失败的调用去重试。
func (c *Client) SubsetFont(ctx context.Context, cps []rune) (*FontSubset, error) {
	if len(cps) == 0 {
		return nil, errors.New("render: font subset: no codepoints")
	}
	req := fontSubsetRequest{Codepoints: make([]uint32, 0, len(cps))}
	for _, r := range cps {
		req.Codepoints = append(req.Codepoints, uint32(r))
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var resp fontSubsetResponse
	if err := c.do(ctx, http.MethodPost, "/v1/fonts/subset", body, &resp); err != nil {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrFontSubsetUnavailable, httpErr.Message)
		}
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("render: font subset: decode payload: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("render: font subset: empty payload")
	}
	out := &FontSubset{
		Family:      resp.Family,
		Format:      resp.Format,
		BaseVersion: resp.BaseVersion,
		Data:        data,
		Codepoints:  resp.Codepoints,
	}
	for _, cp := range resp.Missing {
		out.Missing = append(out.Missing, rune(cp))
	}
	return out, nil
}

type fontSubsetRequest struct {
	Codepoints []uint32 `json:"codepoints"`
}

type fontSubsetResponse struct {
	Family      string `json:"family"`
	Format      string `json:"format"`
	BaseVersion string `json:"baseVersion,omitempty"`
	// Data 是 base64 的字体字节。用 JSON 而非 octet-stream，是为了让缺字报告
	// 与字体字节在同一个响应里——分成两次请求会让「字体与报告对不上」成为可能。
	Data       string   `json:"data"`
	Codepoints int      `json:"codepoints"`
	Missing    []uint32 `json:"missing,omitempty"`
}

// handleFontSubset 处理 POST /v1/fonts/subset。
func (s *Service) handleFontSubset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	subsetter := s.cfg.FontSubsetter
	if subsetter == nil {
		writeError(w, http.StatusNotFound, "not_found", "", "字体子集未启用")
		return
	}
	var req fontSubsetRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "codepoints", "请求体不是合法 JSON")
		return
	}
	if len(req.Codepoints) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "codepoints", "codepoints 不能为空")
		return
	}
	if len(req.Codepoints) > subsetter.MaxCodepoints() {
		writeError(w, http.StatusRequestEntityTooLarge, "too_many_codepoints", "codepoints",
			fmt.Sprintf("最多 %d 个码位", subsetter.MaxCodepoints()))
		return
	}
	cps := make([]rune, 0, len(req.Codepoints))
	for _, cp := range req.Codepoints {
		if cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF) {
			writeError(w, http.StatusBadRequest, "invalid_request", "codepoints",
				fmt.Sprintf("非法码位 U+%04X", cp))
			return
		}
		cps = append(cps, rune(cp))
	}

	out, err := subsetter.Subset(r.Context(), cps)
	if err != nil {
		status := http.StatusInternalServerError
		if r.Context().Err() != nil {
			status = http.StatusRequestTimeout
		}
		writeError(w, status, "font_subset_failed", "", err.Error())
		return
	}
	resp := fontSubsetResponse{
		Family:      out.Family,
		Format:      out.Format,
		BaseVersion: out.BaseVersion,
		Data:        base64.StdEncoding.EncodeToString(out.Data),
		Codepoints:  out.Codepoints,
	}
	for _, r := range out.Missing {
		resp.Missing = append(resp.Missing, uint32(r))
	}
	writeJSON(w, http.StatusOK, resp)
}

// normalizeCodepoints 去重并升序：字集是集合，顺序不该影响缓存键与产物。
func normalizeCodepoints(cps []rune) []rune {
	seen := make(map[rune]struct{}, len(cps))
	out := make([]rune, 0, len(cps))
	for _, r := range cps {
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// formatCodepointList 输出每行一个十六进制码位，供脚本解析。
// 用文本而不是二进制：出错时人能直接打开看，不必再写一个解码器。
func formatCodepointList(cps []rune) string {
	var b strings.Builder
	for _, r := range cps {
		fmt.Fprintf(&b, "%X\n", r)
	}
	return b.String()
}

func orErr(err error, fallback string) error {
	if err != nil {
		return err
	}
	return errors.New(fallback)
}
