package design

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// 资产种类白名单。新增种类必须同时改渲染器，否则会出现"清单里有、画不出来"。
const (
	AssetIllustration = "illustration"
	AssetIcon         = "icon"
	AssetCard         = "card"
	AssetChart        = "chart"
	AssetPhoto        = "photo"
)

// 许可白名单（设计方案 §12.3）。不在集合内的一律拒绝：
// 许可问题必须在数据层拦住，不能靠流程自觉。
const (
	LicenseSelf       = "self"       // 自产
	LicenseOFL        = "OFL"        // SIL Open Font License
	LicenseCC0        = "CC0"        // 公有领域
	LicenseCCBY       = "CC-BY"      // 署名
	LicenseCommercial = "commercial" // 商用授权
)

var assetKinds = map[string]bool{
	AssetIllustration: true, AssetIcon: true, AssetCard: true,
	AssetChart: true, AssetPhoto: true,
}

var assetLicenses = map[string]bool{
	LicenseSelf: true, LicenseOFL: true, LicenseCC0: true,
	LicenseCCBY: true, LicenseCommercial: true,
}

var assetExts = map[string]bool{
	".svg": true, ".png": true, ".webp": true, ".jpg": true, ".jpeg": true,
}

// Asset 是进入成片的图像/图标/插画资产登记项。渲染时按 license 校验。
type Asset struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Version     int    `json:"version"`
	License     string `json:"license"`
	Attribution string `json:"attribution,omitempty"` // CC-BY 必填
	File        string `json:"file"`
}

// Validate 校验单项资产。
func (a Asset) Validate() error {
	if strings.TrimSpace(a.ID) == "" {
		return fmt.Errorf("design: 资产缺少 id")
	}
	if !assetKinds[a.Kind] {
		return fmt.Errorf("design: 资产 %s 的种类 %q 不在允许集合内（illustration|icon|card|chart|photo）", a.ID, a.Kind)
	}
	if a.Version <= 0 {
		return fmt.Errorf("design: 资产 %s 的 version 必须为正（当前 %d）", a.ID, a.Version)
	}
	if strings.TrimSpace(a.License) == "" {
		return fmt.Errorf("design: 资产 %s 缺少 license（§12.3 要求登记许可）", a.ID)
	}
	if !assetLicenses[a.License] {
		return fmt.Errorf("design: 资产 %s 的 license %q 不在允许集合内（self|OFL|CC0|CC-BY|commercial）", a.ID, a.License)
	}
	if a.License == LicenseCCBY && strings.TrimSpace(a.Attribution) == "" {
		return fmt.Errorf("design: 资产 %s 为 CC-BY，必须提供 attribution（署名不能等到导出时再补）", a.ID)
	}
	if strings.TrimSpace(a.File) == "" {
		return fmt.Errorf("design: 资产 %s 缺少 file", a.ID)
	}
	if ext := strings.ToLower(path.Ext(a.File)); !assetExts[ext] {
		return fmt.Errorf("design: 资产 %s 的文件扩展名 %q 不受支持（svg|png|webp|jpg）", a.ID, ext)
	}
	return nil
}

type assetsFile struct {
	Version int     `json:"version"`
	Assets  []Asset `json:"assets"`
}

func decodeAssets(data []byte) ([]Asset, error) {
	var f assetsFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("design: 资产清单 JSON 解析失败: %w", err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("design: 资产清单版本必须为 1，实际 %d", f.Version)
	}
	return f.Assets, nil
}

// ParseAssets 解析资产清单并逐项校验。id 必须唯一。
func ParseAssets(data []byte) ([]Asset, error) {
	list, err := decodeAssets(data)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, a := range list {
		if err := a.Validate(); err != nil {
			return nil, err
		}
		if seen[a.ID] {
			return nil, fmt.Errorf("design: 资产 id 重复: %s", a.ID)
		}
		seen[a.ID] = true
	}
	return list, nil
}
