package project

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
)

// ReadSlideSize 从 PPTX 的 ppt/presentation.xml 读 p:sldSz（EMU）。
//
// 不走 go-pptx：Presentation.slideSize 未导出，发行包钉的是 v2.0.3。
// 找不到或无效时返回 0,0,nil——缺尺寸按标准页高推断，不当成文件损坏。
func ReadSlideSize(r io.ReaderAt, size int64) (widthEMU, heightEMU int64, err error) {
	if r == nil || size <= 0 {
		return 0, 0, nil
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return 0, 0, fmt.Errorf("project: slide size zip: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != "ppt/presentation.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return 0, 0, fmt.Errorf("project: open presentation.xml: %w", err)
		}
		w, h, err := parseSldSz(rc)
		_ = rc.Close()
		return w, h, err
	}
	return 0, 0, nil
}

// FillSlideSizeFromPPTX 在 Document 没有页尺寸时从源 PPTX 补上。只读 sldSz，不重跑 Inspect。
func FillSlideSizeFromPPTX(doc *Document, r io.ReaderAt, size int64) {
	if doc == nil || (doc.SlideWidthEMU > 0 && doc.SlideHeightEMU > 0) {
		return
	}
	w, h, err := ReadSlideSize(r, size)
	if err != nil || w <= 0 || h <= 0 {
		return
	}
	doc.SlideWidthEMU, doc.SlideHeightEMU = w, h
}

const (
	emuPerInch = 914400
	// rasterMinDPI 是历史默认：13.3" 16:9 在 150 DPI 下约 2000×1125，对着 1080p 略缩小。
	rasterMinDPI = 150
	// rasterMaxDPI 与特写重渲对齐，拦住名片尺寸页把解析撑到 4K。
	rasterMaxDPI     = 300
	rasterMinLongPx  = 1920
	rasterMinShortPx = 1080
)

// RasterDPI 让页 PNG 长边至少 1920、短边至少 1080，这样预览/成片 1080p 不再放大 150 DPI 的 10" 页。
// 宽或高为 0 时退回 150。Office 默认 10"×5.625" 16:9 得到 192。
func RasterDPI(widthEMU, heightEMU int64) int {
	if widthEMU <= 0 || heightEMU <= 0 {
		return rasterMinDPI
	}
	wIn := float64(widthEMU) / emuPerInch
	hIn := float64(heightEMU) / emuPerInch
	longIn, shortIn := wIn, hIn
	if hIn > wIn {
		longIn, shortIn = hIn, wIn
	}
	dpi := int(math.Ceil(math.Max(float64(rasterMinLongPx)/longIn, float64(rasterMinShortPx)/shortIn)))
	if dpi < rasterMinDPI {
		return rasterMinDPI
	}
	if dpi > rasterMaxDPI {
		return rasterMaxDPI
	}
	return dpi
}

// RasterSize 是该 DPI 下页图应有的像素。Office 默认 10"×5.625" 在 192 DPI 是 1920×1080。
// pdftoppm 常多吐 1 行（1920×1081）；调用方按这个尺寸裁掉多余的底/右边缘，不要把 1px 双线性拉伸进 1080p。
// 宽高或 DPI 非正则返回 0,0。
func RasterSize(widthEMU, heightEMU int64, dpi int) (width, height int) {
	if widthEMU <= 0 || heightEMU <= 0 || dpi <= 0 {
		return 0, 0
	}
	w := int(math.Round(float64(widthEMU) / emuPerInch * float64(dpi)))
	h := int(math.Round(float64(heightEMU) / emuPerInch * float64(dpi)))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

func parseSldSz(r io.Reader) (int64, int64, error) {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return 0, 0, nil
		}
		if err != nil {
			return 0, 0, fmt.Errorf("project: parse presentation.xml: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "sldSz" {
			continue
		}
		var w, h int64
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "cx":
				w, _ = strconv.ParseInt(a.Value, 10, 64)
			case "cy":
				h, _ = strconv.ParseInt(a.Value, 10, 64)
			}
		}
		if w <= 0 || h <= 0 {
			return 0, 0, nil
		}
		return w, h, nil
	}
}
