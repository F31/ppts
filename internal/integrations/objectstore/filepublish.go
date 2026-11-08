package objectstore

import (
	"context"
	"os"
)

// putFileFallback 在后端不支持文件发布（未实现 FilePublisher）时退回 reader 路径：
// 打开文件交给 Put。多付一次整文件读，但发布语义（原子替换、内容、登记口径）不变。
//
// 加密包装（encryptionStore）不实现 FilePublisher，正是靠这条回退保住语义：
// 文件路径上交给后端的是明文，若内层直接从路径发布，会绕过信封加密把明文落盘。
// 所以回退必须发生在**加密包装之外**——由调用方对「未实现」做类型断言失败处理，
// 而不是让包装层自己偷偷走捷径。
func putFileFallback(ctx context.Context, store ObjectStore, key ObjectKey, path string, meta ObjectMeta) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return store.Put(ctx, key, f, meta)
}
