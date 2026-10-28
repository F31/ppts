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
	"strings"
	"sync"
	"time"
)

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
	Version       string `json:"version"`
	Backend       string `json:"backend"`
	SchemaVersion string `json:"schemaVersion"`
	FPS           int    `json:"maxFps"`
	Fonts         []Font `json:"fonts"`
	MaxConcurrent int    `json:"maxConcurrent"`
	Running       int    `json:"running"`
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
	sem    chan struct{}
}

type job struct {
	id     string
	hash   string
	state  JobState
	prog   Progress
	arts   []string
	err    string
	cancel context.CancelFunc
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
	if cfg.MaxPlanBytes <= 0 {
		cfg.MaxPlanBytes = MaxPlanBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{
		cfg:    cfg,
		jobs:   make(map[string]*job),
		byHash: make(map[string]string),
		sem:    make(chan struct{}, cfg.MaxConcurrent),
	}, nil
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
	running := 0
	for _, j := range s.jobs {
		if j.state == StateRunning || j.state == StateQueued {
			running++
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, Health{
		Version:       s.cfg.Version,
		Backend:       s.cfg.Backend.Name(),
		SchemaVersion: IRSchemaVersion,
		FPS:           MaxFPS,
		Fonts:         s.cfg.Fonts,
		MaxConcurrent: s.cfg.MaxConcurrent,
		Running:       running,
	})
}

func (s *Service) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
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
	id := ShortID(hash)

	s.mu.Lock()
	if existing, ok := s.byHash[hash]; ok {
		j := s.jobs[existing]
		st := JobStatus{ID: j.id, State: j.state, Progress: j.prog, PlanHash: hash}
		s.mu.Unlock()
		// 幂等：同一计划重复提交不重复渲染，直接返回原 job。
		writeJSON(w, http.StatusOK, st)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: id, hash: hash, state: StateQueued,
		prog:   Progress{Done: 0, Total: len(plan.Pages)},
		cancel: cancel}
	s.jobs[id] = j
	s.byHash[hash] = id
	st := JobStatus{ID: id, State: StateQueued, Progress: j.prog, PlanHash: hash}
	s.mu.Unlock()

	go s.run(ctx, j, &plan)

	writeJSON(w, http.StatusAccepted, st)
}

func (s *Service) run(ctx context.Context, j *job, plan *Timeline) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	s.mu.Lock()
	if j.state == StateCanceled {
		s.mu.Unlock()
		return
	}
	j.state = StateRunning
	s.mu.Unlock()

	dir := filepath.Join(s.cfg.WorkDir, j.id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		s.finish(j, StateFailed, fmt.Sprintf("创建工作目录失败: %v", err), nil)
		return
	}

	err := s.cfg.Backend.Render(ctx, plan, dir, func(done, total int) {
		s.mu.Lock()
		j.prog = Progress{Done: done, Total: total}
		s.mu.Unlock()
	})

	if ctx.Err() != nil {
		// 取消是用户意图，不是失败。
		s.finish(j, StateCanceled, "", nil)
		_ = os.RemoveAll(dir)
		return
	}
	if err != nil {
		s.finish(j, StateFailed, err.Error(), nil)
		_ = os.RemoveAll(dir)
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
		return
	}
	j.state = state
	j.err = msg
	j.arts = arts
	if state == StateSucceeded {
		j.prog = Progress{Done: j.prog.Total, Total: j.prog.Total}
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
		if ok && j.state != StateSucceeded && j.state != StateFailed {
			j.state = StateCanceled
			j.cancel()
		}
		s.mu.Unlock()
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "", "job 不存在")
			return
		}
		writeJSON(w, http.StatusOK, JobStatus{ID: id, State: StateCanceled})
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
	id := ShortID(hash)
	s.mu.Lock()
	if existing, ok := s.byHash[hash]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: id, hash: hash, state: StateQueued,
		prog: Progress{Total: len(plan.Pages)}, cancel: cancel}
	s.jobs[id] = j
	s.byHash[hash] = id
	s.mu.Unlock()
	go s.run(ctx, j, plan)
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
		j.state = StateCanceled
		j.cancel()
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
