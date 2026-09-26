package retention

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAssetRegistryCoversCodebaseTypes 是「新增 asset_type 必须登记」的门禁。
//
// 不做这道门禁，新增类型会静默落进 object_inventory 却永不被回收——这正是
// 时间轴/字幕等 12 种类型积累了这么久的原因（架构审视 §3.3）。
// 静态扫描而非靠人记得登记，与 web 侧 i18n:check / api:check 同一思路。
func TestAssetRegistryCoversCodebaseTypes(t *testing.T) {
	root := filepath.Join("..", "..")
	re := regexp.MustCompile(`AssetType:\s*"([a-z_]+)"`)
	found := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			found[m[1]] = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// 扫描本身不能失灵：一条都没扫到说明正则或路径坏了，而不是"没有类型"。
	if len(found) == 0 {
		t.Fatal("no AssetType literals found — the scanner itself is broken")
	}
	for typ, file := range found {
		if _, ok := assetSpecOf(typ); !ok {
			t.Errorf("asset_type %q (used in %s) is not registered in assetRegistry; "+
				"register it or objects of this type will never be recycled", typ, file)
		}
	}
}

func TestRetentionFieldFallback(t *testing.T) {
	cases := map[string]string{
		"artifact": "artifact_retention_days", // 专属档位
		"audio":    "audio_retention_days",
		"render":   "render_retention_days",
		"timeline": derivedRetentionField, // 无专属档位 → 兜底
		"subtitle": derivedRetentionField,
		"source":   "", // 不按龄过期
		"audit":    "",
		"tenant":   "",
		"work":     "",
		"nope":     "", // 未登记类型同样不参与，避免误删
	}
	for typ, want := range cases {
		if got := retentionFieldOf(typ); got != want {
			t.Errorf("retentionFieldOf(%q) = %q, want %q", typ, got, want)
		}
	}
}

func TestDerivedExpiryClauseCoversExpiringTypes(t *testing.T) {
	clause := derivedExpiryClause("$2")
	expiring := 0
	for _, s := range assetRegistry {
		present := strings.Contains(clause, "'"+s.Name+"'")
		if s.Expires {
			expiring++
			if !present {
				t.Errorf("expiring type %q missing from clause", s.Name)
			}
		} else if present {
			t.Errorf("non-expiring type %q must not appear in expiry clause (it has its own lifecycle)", s.Name)
		}
	}
	if expiring == 0 {
		t.Fatal("no expiring types — clause would be FALSE and nothing is ever recycled")
	}
	// 每个可过期类型都必须带 "> 0"：档位 0 的含义是「租户未设档」，
	// 把它当成「立即过期」会一次性删光历史数据（与"未知用 null 不用 0"同源）。
	if got := strings.Count(clause, "> 0"); got != expiring {
		t.Errorf("clause has %d \"> 0\" guards, want %d (one per expiring type): %s", got, expiring, clause)
	}
}

func TestOrphanClauseOnlyCoversTypesWithKnownOwner(t *testing.T) {
	clause := orphanClause("$2")
	for _, s := range assetRegistry {
		present := strings.Contains(clause, "'"+s.Name+"'")
		if s.Owner != nil && !present {
			t.Errorf("type %q has an owner check but is missing from orphan clause", s.Name)
		}
		if s.Owner == nil && present {
			t.Errorf("type %q has no known owner check but appears in orphan clause — "+
				"guessing a reference relation would delete in-use objects", s.Name)
		}
	}
	// 静默期是正确性要求：没有它，"写对象→落清单→提交业务行"窗口内的在途产物会被误判为无主。
	if !strings.Contains(clause, "oi.updated_at < $2") {
		t.Errorf("orphan clause lost its grace-period guard: %s", clause)
	}
}
