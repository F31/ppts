package animation

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// catalog.json 是动效与预设的唯一来源（设计方案 §6.4）。渲染器与编辑器共用同一份，
// 新增动效只需在此追加一项 + 渲染组件，核心校验链路零改动。
//
//go:embed catalog.json
var catalogJSON []byte

// ParamSpec 声明一个动效参数的取值约束。
type ParamSpec struct {
	Type    string // number|enum|bool
	Values  []string
	Min     float64
	Max     float64
	Default ParamDefault
}

// ParamDefault 是参数默认值，保持 JSON 的原始类型以便与 ParamSpec.Type 对齐。
type ParamDefault struct {
	Kind   string // number|string|bool
	Number float64
	String string
	Bool   bool
}

func (p *ParamSpec) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type    string          `json:"type"`
		Values  []string        `json:"values"`
		Min     *float64        `json:"min"`
		Max     *float64        `json:"max"`
		Default json.RawMessage `json:"default"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("animation: param spec: %w", err)
	}
	p.Type = raw.Type
	p.Values = raw.Values
	if raw.Min != nil {
		p.Min = *raw.Min
	}
	if raw.Max != nil {
		p.Max = *raw.Max
	}
	if len(raw.Default) > 0 {
		d, err := parseParamDefault(raw.Default)
		if err != nil {
			return err
		}
		p.Default = d
	}
	return nil
}

func parseParamDefault(data []byte) (ParamDefault, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return ParamDefault{}, fmt.Errorf("animation: param default: %w", err)
	}
	switch t := v.(type) {
	case float64:
		return ParamDefault{Kind: "number", Number: t}, nil
	case string:
		return ParamDefault{Kind: "string", String: t}, nil
	case bool:
		return ParamDefault{Kind: "bool", Bool: t}, nil
	default:
		return ParamDefault{}, fmt.Errorf("animation: unsupported param default type %T", v)
	}
}

// Effect 是一个登记在册的动效。
type Effect struct {
	ID              string               `json:"id"`
	Label           string               `json:"label"`
	Level           string               `json:"level"`
	Class           string               `json:"class"` // entrance|emphasis|exit
	AppliesTo       []string             `json:"appliesTo"`
	Looping         bool                 `json:"looping"`
	Params          map[string]ParamSpec `json:"params"`
	DurationRange   [2]int64             `json:"durationRange"`
	DefaultDuration int64                `json:"defaultDuration"`
}

// Applies 报告该动效是否可用于给定轨道类型（"*" 匹配全部）。
func (e *Effect) Applies(kind string) bool {
	for _, k := range e.AppliesTo {
		if k == "*" || k == kind {
			return true
		}
	}
	return false
}

// ClipTemplate 是预设内的一条 clip 模板；展开时只补 trigger/startMs/id。
type ClipTemplate struct {
	Type       string `json:"type"`
	DurationMs int64  `json:"durationMs"`
	Easing     string `json:"easing"`
	Params     Params `json:"params"`
}

// Preset 是一组默认 clip 参数（不是模板视频，用户改任一项即变为自定义）。
type Preset struct {
	ID             string                    `json:"id"`
	Label          string                    `json:"label"`
	Description    string                    `json:"description"`
	Intensity      float64                   `json:"intensity"`
	StaggerMs      int64                     `json:"staggerMs"`
	SegmentAligned bool                      `json:"segmentAligned"`
	Clips          map[string][]ClipTemplate `json:"clips"`
	// Transition 是套用该预设时的页间转场（L0）。转场不是元素级动效，
	// 不进入可编辑的 Tracks：它由预设整体决定，避免在 V1–V12 里为它开特例。
	Transition TransitionSpec `json:"transition"`
}

// TransitionSpec 是页转场定义。
type TransitionSpec struct {
	Type       string `json:"type"` // none|fade|slide|zoom
	DurationMs int64  `json:"durationMs"`
}

// 允许的转场类型与时长上限。
const (
	TransitionNone  = "none"
	TransitionFade  = "fade"
	TransitionSlide = "slide"
	TransitionZoom  = "zoom"

	// MaxTransitionMS 是转场时长上限：桥接片段必须短，否则会把页内节奏吃掉（§8.4 ≤400ms）。
	MaxTransitionMS = 400
)

// Registry 是解析后的 catalog 视图。
type Registry struct {
	effects  []*Effect
	presets  []*Preset
	effectBy map[string]*Effect
	presetBy map[string]*Preset
}

// Effect 按 id 查动效。
func (r *Registry) Effect(id string) (*Effect, bool) {
	e, ok := r.effectBy[id]
	return e, ok
}

// Preset 按 id 查预设。
func (r *Registry) Preset(id string) (*Preset, bool) {
	p, ok := r.presetBy[id]
	return p, ok
}

// Effects 按 id 字典序返回全部动效（错误提示用，顺序稳定）。
func (r *Registry) Effects() []*Effect {
	out := make([]*Effect, len(r.effects))
	copy(out, r.effects)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Presets 按 id 字典序返回全部预设。
func (r *Registry) Presets() []*Preset {
	out := make([]*Preset, len(r.presets))
	copy(out, r.presets)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// EffectIDs 返回全部动效 id（字典序），用于 V1 的错误提示。
func (r *Registry) EffectIDs() []string {
	ids := make([]string, 0, len(r.effects))
	for _, e := range r.effects {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids
}

// ParseCatalog 解析并自检一份 catalog。自检失败即视为定义错误：
// catalog 是唯一来源，坏了就不该放行任何写入。
func ParseCatalog(data []byte) (*Registry, error) {
	var doc struct {
		SchemaVersion string    `json:"schemaVersion"`
		Effects       []*Effect `json:"effects"`
		Presets       []*Preset `json:"presets"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("animation: parse catalog: %w", err)
	}
	if doc.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("animation: catalog schemaVersion %q, want %q", doc.SchemaVersion, SchemaVersion)
	}
	reg := &Registry{
		effectBy: make(map[string]*Effect, len(doc.Effects)),
		presetBy: make(map[string]*Preset, len(doc.Presets)),
	}
	for _, e := range doc.Effects {
		if e.ID == "" {
			return nil, fmt.Errorf("animation: catalog effect without id")
		}
		if _, dup := reg.effectBy[e.ID]; dup {
			return nil, fmt.Errorf("animation: duplicate effect %q", e.ID)
		}
		if err := validateEffect(e); err != nil {
			return nil, err
		}
		reg.effects = append(reg.effects, e)
		reg.effectBy[e.ID] = e
	}
	for _, p := range doc.Presets {
		if p.ID == "" {
			return nil, fmt.Errorf("animation: catalog preset without id")
		}
		if _, dup := reg.presetBy[p.ID]; dup {
			return nil, fmt.Errorf("animation: duplicate preset %q", p.ID)
		}
		if err := validatePreset(p, reg); err != nil {
			return nil, err
		}
		reg.presets = append(reg.presets, p)
		reg.presetBy[p.ID] = p
	}
	return reg, nil
}

func validateEffect(e *Effect) error {
	switch e.Class {
	case ClassEntrance, ClassEmphasis, ClassExit:
	default:
		return fmt.Errorf("animation: effect %q has invalid class %q", e.ID, e.Class)
	}
	if len(e.AppliesTo) == 0 {
		return fmt.Errorf("animation: effect %q must declare appliesTo", e.ID)
	}
	if e.DurationRange[0] < MinClipDurationMS || e.DurationRange[1] > MaxClipDurationMS ||
		e.DurationRange[0] > e.DurationRange[1] {
		return fmt.Errorf("animation: effect %q durationRange %v outside [%d,%d]",
			e.ID, e.DurationRange, MinClipDurationMS, MaxClipDurationMS)
	}
	if e.DefaultDuration < e.DurationRange[0] || e.DefaultDuration > e.DurationRange[1] {
		return fmt.Errorf("animation: effect %q defaultDuration %d outside %v",
			e.ID, e.DefaultDuration, e.DurationRange)
	}
	for name, spec := range e.Params {
		if err := validateParamSpec(name, spec); err != nil {
			return fmt.Errorf("animation: effect %q: %w", e.ID, err)
		}
	}
	return nil
}

func validateParamSpec(name string, spec ParamSpec) error {
	switch spec.Type {
	case "number":
		if spec.Min > spec.Max {
			return fmt.Errorf("param %q min > max", name)
		}
		if spec.Default.Kind != "number" {
			return fmt.Errorf("param %q default must be a number", name)
		}
		if spec.Default.Number < spec.Min || spec.Default.Number > spec.Max {
			return fmt.Errorf("param %q default %v outside [%v,%v]", name, spec.Default.Number, spec.Min, spec.Max)
		}
	case "enum":
		if len(spec.Values) == 0 {
			return fmt.Errorf("param %q enum without values", name)
		}
		if spec.Default.Kind != "string" {
			return fmt.Errorf("param %q default must be a string", name)
		}
		found := false
		for _, v := range spec.Values {
			if v == spec.Default.String {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("param %q default %q not in values", name, spec.Default.String)
		}
	case "bool":
		if spec.Default.Kind != "bool" {
			return fmt.Errorf("param %q default must be a bool", name)
		}
	default:
		return fmt.Errorf("param %q has unknown type %q", name, spec.Type)
	}
	return nil
}

func validatePreset(p *Preset, reg *Registry) error {
	if p.Intensity < MinIntensity || p.Intensity > MaxIntensity {
		return fmt.Errorf("animation: preset %q intensity %v outside [%v,%v]",
			p.ID, p.Intensity, MinIntensity, MaxIntensity)
	}
	if p.StaggerMs < 0 {
		return fmt.Errorf("animation: preset %q has negative staggerMs", p.ID)
	}
	for kind, clips := range p.Clips {
		if !validKind(kind) {
			return fmt.Errorf("animation: preset %q unknown kind %q", p.ID, kind)
		}
		for _, c := range clips {
			eff, ok := reg.effectBy[c.Type]
			if !ok {
				return fmt.Errorf("animation: preset %q references unknown effect %q", p.ID, c.Type)
			}
			if !eff.Applies(kind) {
				return fmt.Errorf("animation: preset %q uses %q on unsupported kind %q", p.ID, c.Type, kind)
			}
			if c.DurationMs < eff.DurationRange[0] || c.DurationMs > eff.DurationRange[1] {
				return fmt.Errorf("animation: preset %q clip %q duration %d outside %v",
					p.ID, c.Type, c.DurationMs, eff.DurationRange)
			}
			if _, ok := validEasings[c.Easing]; !ok {
				return fmt.Errorf("animation: preset %q clip %q has invalid easing %q", p.ID, c.Type, c.Easing)
			}
		}
	}
	if err := validateTransition(p.Transition); err != nil {
		return fmt.Errorf("animation: preset %q: %w", p.ID, err)
	}
	return nil
}

// validateTransition 校验转场定义。
func validateTransition(t TransitionSpec) error {
	switch t.Type {
	case TransitionNone:
		if t.DurationMs != 0 {
			return fmt.Errorf("transition %q must have durationMs=0", t.Type)
		}
	case TransitionFade, TransitionSlide, TransitionZoom:
		if t.DurationMs <= 0 || t.DurationMs > MaxTransitionMS {
			return fmt.Errorf("transition %q duration %d outside (0,%d]", t.Type, t.DurationMs, MaxTransitionMS)
		}
	default:
		return fmt.Errorf("unknown transition type %q", t.Type)
	}
	return nil
}

// TransitionFor 返回预设的转场；未找到预设时返回 fade 缺省值。
func TransitionFor(presetID string) TransitionSpec {
	if p, ok := Catalog().Preset(presetID); ok {
		return p.Transition
	}
	return TransitionSpec{Type: TransitionFade, DurationMs: 240}
}

// validKind 报告轨道类型是否是登记的 8 种之一。
func validKind(kind string) bool {
	switch kind {
	case KindPage, KindTitle, KindBullet, KindImage, KindIcon, KindChart, KindTable, KindShape:
		return true
	default:
		return false
	}
}

var (
	catalogOnce sync.Once
	catalogReg  *Registry
	catalogErr  error
)

// Catalog 返回内嵌 catalog。内嵌数据在包初始化后不可变，解析失败直接 panic：
// 这是构建期错误，带病启动只会让每次写入都莫名其妙失败。
func Catalog() *Registry {
	catalogOnce.Do(func() {
		catalogReg, catalogErr = ParseCatalog(catalogJSON)
		if catalogErr != nil {
			panic(catalogErr)
		}
	})
	return catalogReg
}
