package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrQueueFull 表示排队任务已达上限，提交被拒绝（HTTP 对应 429）。
var ErrQueueFull = errors.New("render: 渲染队列已满")

// queueRetryAfterSeconds 是 429 里建议的退避秒数。
// 取 5s：比单页渲染耗时短，客户端按契约退避重试两次就能赶上队列消化。
const queueRetryAfterSeconds = 5

// Backend 是渲染实现的 SPI。本包只定义契约，不关心是 Remotion、Chromium 还是别的。
//
// 契约：
//   - 只读 plan，不得修改；
//   - 只往 dir 里写文件，不得访问 dir 之外；
//   - 产物文件名由 backend 决定，服务端按目录内容生成 artifacts 列表；
//   - ctx 取消时尽快返回，已写出的文件由服务端清理。
type Backend interface {
	Name() string
	Render(ctx context.Context, plan *Timeline, dir string, progress func(done, total int)) error
}

// 任务状态。
type JobState string

const (
	StateQueued    JobState = "queued"
	StateRunning   JobState = "running"
	StateSucceeded JobState = "succeeded"
	StateFailed    JobState = "failed"
	StateCanceled  JobState = "canceled"
)

// Progress 是渲染进度（按页计）。
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// JobStatus 是 GET /v1/render/{id} 的响应体。
type JobStatus struct {
	ID        string   `json:"jobId"`
	State     JobState `json:"state"`
	Progress  Progress `json:"progress"`
	PlanHash  string   `json:"planHash,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
	Error     string   `json:"error,omitempty"`
	// Degraded 为 true 表示服务内部降级（例如渲染器缺失字体），产物可能不完整。
	Degraded bool `json:"degraded,omitempty"`
}

// Health 是 GET /healthz 的响应体（§8.6 能力探测）。
type Health struct {
	Version string `json:"version"`
	Backend string `json:"backend"`
	// ChromeVersion 是浏览器版本，仅光栅后端会填。它不进 planHash（Version 里已经含了），
	// 但漏洞台账与「升级后像素为什么变了」的排查需要它单独可读。
	ChromeVersion string `json:"chromeVersion,omitempty"`
	SchemaVersion string `json:"schemaVersion"`
	FPS           int    `json:"maxFps"`
	Fonts         []Font `json:"fonts"`
	MaxConcurrent int    `json:"maxConcurrent"`
	// Running 只数正在跑的任务；排队中的单独报 Queued。
	// 把 queued 混进 running 会让「到底有几个渲染进程在吃内存」这个最该看数的指标失真。
	Running   int `json:"running"`
	Queued    int `json:"queued"`
	MaxQueued int `json:"maxQueued"`
	// Retained 是仍可查询的终态任务数（受 JobTTL 约束）。
	Retained int `json:"retained"`
}

// ServiceConfig 是渲染服务配置。
type ServiceConfig struct {
	Backend Backend
	// WorkDir 是唯一允许写文件的根目录。
	WorkDir string
	// Version 是渲染服务版本，参与 planHash：换版本必须重渲。
	Version string
	// Fonts 是镜像内可用字体清单，参与 planHash。
	Fonts []Font
	// MaxConcurrent 是同时渲染的任务数上限（0 = 2）。
	MaxConcurrent int
	// MaxQueued 是排队中（queued）任务数上限（0 = 8）。
	// 它约束的是「已受理、尚未起跑」的计划占用：每个排队任务持有一份整计划（上限 MaxPlanBytes）。
	MaxQueued int
	// JobTTL 是终态任务（含产物目录）的保留时长（0 = 30 分钟），从落终态开始计时。
	// 超过后 job 查不到、产物被删除；调用方 MUST 在 TTL 内取走产物。
	JobTTL time.Duration
	// RetentionSweep 是惰性回收的最小间隔（0 = 1 分钟）。
	RetentionSweep time.Duration
	// MaxPlanBytes 是请求体上限（0 = MaxPlanBytes）。
	MaxPlanBytes int64
	// Now 便于测试注入时钟（0 = time.Now）。
	Now func() time.Time
}

// Service 是渲染服务的 HTTP 实现。
type Service struct {
	cfg ServiceConfig

	mu     sync.Mutex
	jobs   map[string]*job
	byHash map[string]string
	// attempts 按 planHash 前 24 位计数，用于给重试分配新 id（首次不加后缀）。
	attempts map[string]*attemptStat
	sem      chan struct{}
	// lastSweep 是上一次回收时间：把回收摊在请求上，而不是起一条常驻 goroutine。
	lastSweep time.Time
}

// attemptStat 记录一个 planHash 前缀的分配情况。
// live 归零时整条删除——attempts 不能只增不减，否则它自己就是第三张泄漏的表。
type attemptStat struct {
	next int
	live int
}

type job struct {
	id     string
	hash   string
	state  JobState
	prog   Progress
	arts   []string
	err    string
	cancel context.CancelFunc
	// plan 只在「受理 → 起跑」期间持有；起跑后置 nil，由局部变量接管。
	// 若让闭包直接捕获 plan，排队期被取消的任务会把整份计划攥到信号量排到它为止。
	plan *Timeline
	// finishedAt 是落终态的时刻，TTL 从这里起算（不是受理时刻：
	// 一次 15 分钟的长渲染如果从受理算起，成功后马上就会被回收）。
	finishedAt time.Time
}

// NewService 校验配置并返回服务。
func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.Backend == nil {
		return nil, errors.New("render: backend is required")
	}
	if cfg.WorkDir == "" {
		return nil, errors.New("render: work dir is required")
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o750); err != nil {
		return nil, fmt.Errorf("render: create work dir: %w", err)
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}
	if cfg.MaxQueued <= 0 {
		cfg.MaxQueued = 8
	}
	if cfg.JobTTL <= 0 {
		cfg.JobTTL = 30 * time.Minute
	}
	if cfg.RetentionSweep <= 0 {
		cfg.RetentionSweep = time.Minute
	}
	if cfg.MaxPlanBytes <= 0 {
		cfg.MaxPlanBytes = MaxPlanBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{
		cfg:      cfg,
		jobs:     make(map[string]*job),
		byHash:   make(map[string]string),
		attempts: make(map[string]*attemptStat),
		sem:      make(chan struct{}, cfg.MaxConcurrent),
	}, nil
}

// allocID 给这份计划分配一个 job id：首次用 ShortID(hash)，重试追加 -rN 后缀。
//
// 重试 MUST 落在新目录：上一轮被取消的 goroutine 可能还在清理自己的目录，
// 若两轮共用同一个目录，旧轮的 RemoveAll 会把新轮的产物删掉。
// 调用方必须已持有 s.mu。
func (s *Service) allocID(hash string) string {
	base := ShortID(hash)
	st := s.attempts[base]
	if st == nil {
		st = &attemptStat{}
		s.attempts[base] = st
	}
	n := st.next
	st.next++
	st.live++
	if n == 0 {
		return base
	}
	return fmt.Sprintf("%s-r%d", base, n+1)
}

// queuedLocked 返回排队中的任务数。调用方必须已持有 s.mu。
func (s *Service) queuedLocked() int {
	n := 0
	for _, j := range s.jobs {
		if j.state == StateQueued {
			n++
		}
	}
	return n
}

// queueFull 判断新提交是否该被容量拒绝。
func (s *Service) queueFull() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queuedLocked() >= s.cfg.MaxQueued
}

// writeQueueFull 回 429 + Retry-After。
//
// 选 429 而不是 503：契约里 429 已经是「可退避重试」，503 表示服务本身坏了。
// 队列满不是故障，是流量超过了容量，客户端该做的是等一会儿再来。
func (s *Service) writeQueueFull(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(queueRetryAfterSeconds))
	writeError(w, http.StatusTooManyRequests, "queue_full", "",
		fmt.Sprintf("渲染队列已满（排队上限 %d），请稍后重试", s.cfg.MaxQueued))
}

// maybeSweep 按 RetentionSweep 的节奏惰性回收过期任务。
//
// 不起后台 goroutine 是刻意的：Service 没有 Close 生命周期，起一条 ticker
// 就等于给每个测试进程留一条永不退出的协程；而回收只在有请求时才有意义——
// 没有请求，就没有新的内存与磁盘增长。
func (s *Service) maybeSweep() {
	s.mu.Lock()
	now := s.cfg.Now()
	if !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < s.cfg.RetentionSweep {
		s.mu.Unlock()
		return
	}
	s.lastSweep = now
	ids := s.reapLocked(now)
	s.mu.Unlock()
	s.removeJobDirs(ids)
}

// sweepExpired 立即回收所有过期终态任务，返回被回收的 job id。
// 测试直接调用它，不依赖时钟推进与节流节奏。
func (s *Service) sweepExpired(now time.Time) []string {
	s.mu.Lock()
	s.lastSweep = now
	ids := s.reapLocked(now)
	s.mu.Unlock()
	s.removeJobDirs(ids)
	return ids
}

// reapLocked 摘除过期任务的记录与映射，返回待删目录对应的 id。
// 调用方必须已持有 s.mu；目录删除放在锁外，避免 RemoveAll 拖住提交。
func (s *Service) reapLocked(now time.Time) []string {
	var ids []string
	for id, j := range s.jobs {
		// 只有终态才回收：running 正在写目录，queued 还没有目录。
		if j.finishedAt.IsZero() {
			continue
		}
		if now.Sub(j.finishedAt) < s.cfg.JobTTL {
			continue
		}
		delete(s.jobs, id)
		s.releaseHash(j)
		s.dropAttempt(j.hash)
		ids = append(ids, id)
	}
	return ids
}

func (s *Service) removeJobDirs(ids []string) {
	for _, id := range ids {
		_ = os.RemoveAll(filepath.Join(s.cfg.WorkDir, id))
	}
}

// dropAttempt 在任务被回收时减计数，归零即删。
// 调用方必须已持有 s.mu。
func (s *Service) dropAttempt(hash string) {
	base := ShortID(hash)
	st := s.attempts[base]
	if st == nil {
		return
	}
	st.live--
	if st.live <= 0 {
		delete(s.attempts, base)
	}
}

// Handler 返回 HTTP 路由。路径与 §8.2 一致，额外提供 /healthz。
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/render", s.handleSubmit)
	mux.HandleFunc("/v1/render/", s.handleJob)
	return mux
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	s.mu.Lock()
	running, queued, retained := 0, 0, 0
	for _, j := range s.jobs {
		switch j.state {
		case StateQueued:
			queued++
		case StateRunning:
			running++
		default:
			retained++
		}
	}
	s.mu.Unlock()
	// 浏览器版本由 backend 自己报：服务层不该知道 Chromium 的存在。
	chromeVersion := ""
	if reporter, ok := s.cfg.Backend.(interface{ ChromeVersion() string }); ok {
		chromeVersion = reporter.ChromeVersion()
	}
	writeJSON(w, http.StatusOK, Health{
		Version:       s.cfg.Version,
		Backend:       s.cfg.Backend.Name(),
		ChromeVersion: chromeVersion,
		SchemaVersion: IRSchemaVersion,
		FPS:           MaxFPS,
		Fonts:         s.cfg.Fonts,
		MaxConcurrent: s.cfg.MaxConcurrent,
		Running:       running,
		Queued:        queued,
		MaxQueued:     s.cfg.MaxQueued,
		Retained:      retained,
	})
}

func (s *Service) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	s.maybeSweep()
	// 容量在读取请求体之前判：队列满时把 4MB 读进来只为回一句 429 是纯浪费，
	// 更糟的是每个被拒请求都要在内存里留一份 plan 直到 GC。
	//
	// 代价：超载期间「同一份计划的幂等重复提交」也可能拿到 429。
	// 这是可接受的——429 在客户端契约里本来就可重试，而容量优先于去重。
	if s.queueFull() {
		s.writeQueueFull(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, s.cfg.MaxPlanBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_plan", "", "读取请求体失败")
		return
	}
	if int64(len(body)) > s.cfg.MaxPlanBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "plan_too_large", "",
			fmt.Sprintf("计划超过 %d 字节上限", s.cfg.MaxPlanBytes))
		return
	}

	var plan Timeline
	dec := json.NewDecoder(strings.NewReader(string(body)))
	// 拒绝未知字段：多出来的字段意味着调用方与服务端契约不一致，静默忽略会掩盖漂移。
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_plan", "", err.Error())
		return
	}
	if err := plan.Validate(); err != nil {
		var pe *PlanError
		if errors.As(err, &pe) {
			writeError(w, http.StatusBadRequest, pe.Code, pe.Field, pe.Message)
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_plan", "", err.Error())
		return
	}

	hash, err := plan.Hash(HashOptions{RendererVersion: s.cfg.Version})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash_failed", "", err.Error())
		return
	}
	s.mu.Lock()
	if existing, ok := s.byHash[hash]; ok {
		j := s.jobs[existing]
		st := JobStatus{ID: j.id, State: j.state, Progress: j.prog, PlanHash: hash}
		s.mu.Unlock()
		// 幂等：同一计划重复提交不重复渲染，直接返回原 job。
		writeJSON(w, http.StatusOK, st)
		return
	}
	// 预检到入队之间可能又挤进来几个请求：入队前必须在锁内再判一次，
	// 否则上限只是个装饰。判定排在 allocID 之前——被拒的提交不该消耗重试编号。
	if s.queuedLocked() >= s.cfg.MaxQueued {
		s.mu.Unlock()
		s.writeQueueFull(w)
		return
	}
	id := s.allocID(hash)
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: id, hash: hash, state: StateQueued,
		prog:   Progress{Done: 0, Total: len(plan.Pages)},
		cancel: cancel, plan: &plan}
	s.jobs[id] = j
	s.byHash[hash] = id
	st := JobStatus{ID: id, State: StateQueued, Progress: j.prog, PlanHash: hash}
	s.mu.Unlock()

	go s.run(ctx, j)

	writeJSON(w, http.StatusAccepted, st)
}

func (s *Service) run(ctx context.Context, j *job) {
	// 等槽位时也监听取消：否则「提交后立刻取消」会留下一条卡在信号量上的协程，
	// 一直排到它前面所有任务都跑完才退出。取消风暴能靠这个把协程堆起来。
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		s.finish(j, StateCanceled, "", nil)
		return
	}
	defer func() { <-s.sem }()

	dir := filepath.Join(s.cfg.WorkDir, j.id)

	s.mu.Lock()
	plan := j.plan
	if j.state == StateCanceled || plan == nil {
		s.mu.Unlock()
		// 排队期就被取消：清掉可能残留的目录再落终态。
		_ = os.RemoveAll(dir)
		s.finish(j, StateCanceled, "", nil)
		return
	}
	j.state = StateRunning
	// 起跑后由局部变量持有计划；job 里再留一份只会延长它的生命周期。
	j.plan = nil
	s.mu.Unlock()

	// 先清空本 job 的目录：同一个 id 可能是「失败/取消后重试」，
	// 上一轮写了一半的片段会让 verifyMP4 误判成可播放产物。
	if err := os.RemoveAll(dir); err != nil {
		s.finish(j, StateFailed, fmt.Sprintf("清理工作目录失败: %v", err), nil)
		return
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		s.finish(j, StateFailed, fmt.Sprintf("创建工作目录失败: %v", err), nil)
		return
	}

	err := s.cfg.Backend.Render(ctx, plan, dir, func(done, total int) {
		s.mu.Lock()
		j.prog = Progress{Done: done, Total: total}
		s.mu.Unlock()
	})

	// 清理排在 finish 之前：对外宣告终态时，这轮的半成品文件应当已经消失，
	// 客户端不会看到「已失败但目录里还摆着能打开的片段」。
	if ctx.Err() != nil {
		// 取消是用户意图，不是失败。
		_ = os.RemoveAll(dir)
		s.finish(j, StateCanceled, "", nil)
		return
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		s.finish(j, StateFailed, err.Error(), nil)
		return
	}

	arts, err := listArtifacts(dir)
	if err != nil {
		s.finish(j, StateFailed, fmt.Sprintf("收集产物失败: %v", err), nil)
		return
	}
	s.finish(j, StateSucceeded, "", arts)
}

// listArtifacts 按文件名排序返回相对路径：产物列表必须确定，否则客户端对比两次结果会误判。
func listArtifacts(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}

func (s *Service) finish(j *job, state JobState, msg string, arts []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 已取消的任务不允许被后到的结果覆盖：否则用户点了取消却看到"成功"。
	if j.state == StateCanceled && state != StateCanceled {
		s.releaseHash(j)
		return
	}
	j.state = state
	j.err = msg
	j.arts = arts
	j.finishedAt = s.cfg.Now()
	j.plan = nil
	if state == StateSucceeded {
		j.prog = Progress{Done: j.prog.Total, Total: j.prog.Total}
		return
	}
	// 失败与取消 MUST 释放 planHash：渲染会因 Chrome 崩溃、超时、OOM 失败，
	// 取消也常是误触。若重试仍命中这个旧 job，这份计划就再也渲不出来了。
	// 只有「成功」才配享受幂等复用——那也是唯一能让 byHash 带来收益的终态。
	s.releaseHash(j)
}

// releaseHash 解除 planHash → job 的映射。调用方必须已持有 s.mu。
func (s *Service) releaseHash(j *job) {
	if s.byHash[j.hash] == j.id {
		delete(s.byHash, j.hash)
	}
}

func (s *Service) handleJob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/render/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not_found", "", "job 不存在")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.maybeSweep()
		s.mu.Lock()
		j, ok := s.jobs[id]
		if !ok {
			s.mu.Unlock()
			writeError(w, http.StatusNotFound, "not_found", "", "job 不存在")
			return
		}
		st := JobStatus{ID: j.id, State: j.state, Progress: j.prog, PlanHash: j.hash,
			Artifacts: j.arts, Error: j.err}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, st)
	case http.MethodDelete:
		s.mu.Lock()
		j, ok := s.jobs[id]
		if !ok {
			s.mu.Unlock()
			writeError(w, http.StatusNotFound, "not_found", "", "job 不存在")
			return
		}
		if j.state != StateSucceeded && j.state != StateFailed {
			if j.state != StateCanceled {
				j.state = StateCanceled
				j.finishedAt = s.cfg.Now()
				// 排队中的任务直接丢掉计划引用：协程可能还堵在信号量上，
				// 不为它保留 4MB。
				j.plan = nil
				j.cancel()
			}
			// 立刻释放 planHash：取消后马上重试是最常见的动作。
			// 重试会拿到新 id + 新目录，与这个 job 残留的清理互不干扰。
			s.releaseHash(j)
		}
		// 回实际状态而不是硬编码 canceled：对已完成的任务点取消，它并没有被取消，
		// 说成 canceled 只会让客户端误以为产物随时会消失。
		st := JobStatus{ID: j.id, State: j.state}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, st)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodDelete)
	}
}

// Submit 是进程内调用入口（供测试与本地编排）。语义与 POST /v1/render 相同。
func (s *Service) Submit(plan *Timeline) (string, error) {
	if err := plan.Validate(); err != nil {
		return "", err
	}
	hash, err := plan.Hash(HashOptions{RendererVersion: s.cfg.Version})
	if err != nil {
		return "", err
	}
	s.maybeSweep()
	s.mu.Lock()
	if existing, ok := s.byHash[hash]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	if s.queuedLocked() >= s.cfg.MaxQueued {
		s.mu.Unlock()
		return "", ErrQueueFull
	}
	id := s.allocID(hash)
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: id, hash: hash, state: StateQueued,
		prog: Progress{Total: len(plan.Pages)}, cancel: cancel, plan: plan}
	s.jobs[id] = j
	s.byHash[hash] = id
	s.mu.Unlock()
	go s.run(ctx, j)
	return id, nil
}

// Status 返回任务状态（进程内调用）。
func (s *Service) Status(id string) (JobStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return JobStatus{}, false
	}
	return JobStatus{ID: j.id, State: j.state, Progress: j.prog, PlanHash: j.hash,
		Artifacts: j.arts, Error: j.err}, true
}

// Cancel 取消任务（进程内调用）。
func (s *Service) Cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return false
	}
	if j.state != StateSucceeded && j.state != StateFailed {
		if j.state != StateCanceled {
			j.state = StateCanceled
			j.finishedAt = s.cfg.Now()
			j.plan = nil
			j.cancel()
		}
		// 与 DELETE 同理：取消必须立刻释放 planHash，否则重试会命中这个已取消的 job。
		s.releaseHash(j)
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, field, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "field": field, "message": msg})
}

func methodNotAllowed(w http.ResponseWriter, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "", "方法不允许")
}
