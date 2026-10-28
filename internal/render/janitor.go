package render

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// jobDirName 判定一个目录名是否由本服务分配（ShortID + 可选的重试后缀）。
//
// 只认自己造的名字，是为了让 WorkDir 能和别的东西共处：运维放的 dump、
// selftest 的 os.MkdirTemp("selftest-*")、文件系统自带的 lost+found
// 都不该在重启时被顺手删掉。宁可漏删一个不认识的目录，也不能删错。
var jobDirName = regexp.MustCompile(`^[0-9a-f]{24}(-r[1-9][0-9]*)?$`)

// OrphanReport 是一次启动清扫的结果。
type OrphanReport struct {
	// Removed 是成功删除的目录数，Failed 是删除失败的目录数。
	Removed int
	Failed  int
	// Bytes 是删除成功的目录内容合计，用于回答「这次重启回收了多少磁盘」。
	Bytes int64
}

// Empty 表示这次清扫什么都没做（没有孤儿，或清扫被关闭）。
func (r OrphanReport) Empty() bool { return r.Removed == 0 && r.Failed == 0 }

// cleanOrphanDirs 删除 WorkDir 下不属于本进程的 job 目录。
//
// 为什么是「启动时全删」而不是「持久化索引」：jobs 只在内存，进程一重启，
// 所有 id 当场失效——即使把产物留在盘上，也没有任何客户端能凭 id 取到它
// （契约里 id 只在进程生命周期 + JobTTL 内有效）。于是这些目录既查不到、
// 又不敢删，只会让磁盘单调增长。本服务保持无状态：重启 = 上一轮一切作废。
//
// 隐含前提是单副本部署。多个进程共用一个 WorkDir 时，先起的副本会把别的
// 副本正在写的目录删掉——那种部署必须用 ServiceConfig.KeepOrphans 关掉清扫。
//
// 清扫失败不阻塞启动：一个删不掉的目录（Windows 上句柄未释放、权限不足）
// 不该让整个服务起不来。记进 Failed 交给调用方打日志，下次重启再试。
// 该方法的唯一合法调用时机是 NewService（此时 jobs 必为空）。
func (s *Service) cleanOrphanDirs() OrphanReport {
	var rep OrphanReport
	entries, err := os.ReadDir(s.cfg.WorkDir)
	if err != nil {
		// WorkDir 刚 MkdirAll 过，读不到通常意味着权限或挂载出了问题。
		// 报出来但继续：服务能起来、能报错，比起不来更容易诊断。
		rep.Failed = 1
		s.logf("render: 扫描工作目录 %s 失败，跳过启动清扫: %v", s.cfg.WorkDir, err)
		return rep
	}
	for _, e := range entries {
		// 只删目录：os.RemoveAll 对符号链接只删链接本身，但既然 entry 不是
		// 目录就不属于本服务的产物形态，交给运维判断，不主动触碰。
		if !e.IsDir() || !jobDirName.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(s.cfg.WorkDir, e.Name())
		// 先量体积再删：删完就无从知道回收了多少。
		size := dirBytes(dir)
		if err := os.RemoveAll(dir); err != nil {
			rep.Failed++
			s.logf("render: 清理孤儿目录 %s 失败（下次重启重试）: %v", e.Name(), err)
			continue
		}
		rep.Removed++
		rep.Bytes += size
	}
	if rep.Removed > 0 {
		s.logf("render: 启动清扫删除 %d 个孤儿目录，释放 %s", rep.Removed, humanBytes(rep.Bytes))
	}
	return rep
}

// humanBytes 在小体积时退到 KiB：清扫日志里出现 "释放 0.0 MiB" 会让人以为没清掉。
func humanBytes(n int64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// dirBytes 累计目录下普通文件的大小。读不到的条目按 0 计——
// 这里只是给日志一个量级，不该因为某个文件没权限就让清扫失败。
func dirBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (s *Service) logf(format string, args ...any) {
	if s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
	}
}
