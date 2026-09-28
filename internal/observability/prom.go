package observability

import (
	"encoding/json"
	"expvar"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

// authEventsTotal 记录认证生命周期事件（register_success/register_blocked/login_success/
// login_failure/login_locked/verify_email/password_reset 等）。
var authEventsTotal = expvar.NewMap("ppts_auth_events_total")

// AuthEvent 记录一次认证事件（低基数标签）。
func AuthEvent(event string) { authEventsTotal.Add(clean(event), 1) }

// textNormDictEventsTotal 记录文本规范化词典加载事件（R1）：
// load_error / empty_unexpected —— 依赖不可用必须可观测，不能退回静默。
var textNormDictEventsTotal = expvar.NewMap("ppts_textnorm_dict_events_total")

// TextNormDictEvent 记录一次文本规范化词典事件。
func TextNormDictEvent(event string) { textNormDictEventsTotal.Add(clean(event), 1) }

// PrometheusHandler 把进程内 expvar 指标渲染为 Prometheus 文本格式（0.0.4）。
//
// 约定：expvar.Map 的键以 "k=v,k2=v2" 形式表达标签；标量键（无 "="）降级为 key="..." 标签。
// 不引入 prometheus/client_golang 依赖；指标名/标签名做合法性清洗。
func PrometheusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		expvar.Do(func(kv expvar.KeyValue) {
			name := sanitizeMetricName(kv.Key)
			writePrometheus(w, name, kv.Value.String())
		})
	})
}

func writePrometheus(w io.Writer, name, raw string) {
	if raw == "" {
		return
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		fmt.Fprintf(w, "# TYPE %s gauge\n%s %s\n", name, name, formatFloat(f))
		return
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err == nil {
		fmt.Fprintf(w, "# TYPE %s gauge\n", name)
		// 排序键以保证输出稳定（便于 diff / 抓取去重）。
		for _, k := range sortedKeys(m) {
			switch v := m[k].(type) {
			case float64:
				fmt.Fprintf(w, "%s%s %s\n", name, labelsFor(k), formatFloat(v))
			case map[string]any:
				for _, k2 := range sortedKeys(v) {
					if f, ok := v[k2].(float64); ok {
						fmt.Fprintf(w, "%s%s %s\n", name, labelsFor(k+","+k2), formatFloat(f))
					}
				}
			}
		}
		return
	}
	// 字符串型 expvar：以 _info 暴露。
	fmt.Fprintf(w, "# TYPE %s_info gauge\n%s_info{value=%q} 1\n", name, name, raw)
}

func labelsFor(key string) string {
	if key == "" {
		return ""
	}
	var parts []string
	for _, kv := range strings.Split(key, ",") {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", sanitizeLabelName(kv[:i]), kv[i+1:]))
	}
	if len(parts) == 0 {
		return fmt.Sprintf(`{key=%q}`, key)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 插入排序（键数量极小，避免引入 sort 依赖开销无意义；语义正确即可）。
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func sanitizeMetricName(s string) string {
	var b strings.Builder
	for i, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == ':' || (i > 0 && r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "ppts_unknown"
	}
	return b.String()
}

func sanitizeLabelName(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsLetter(r) || r == '_' || (i > 0 && unicode.IsDigit(r)) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "label"
	}
	return b.String()
}
