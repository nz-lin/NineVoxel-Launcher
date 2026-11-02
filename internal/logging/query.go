// 本文件实现日志的读取与分页查询。
//
// 读取策略：按文件从新到旧扫描，遇到满足条件的记录先收集，再按时间倒序返回。
// 之所以不做索引：日志是追加写的 JSON Lines，单文件 5 MiB、目录上限 50 MiB，
// 一次查询最多扫描数十万行，属于可接受的成本；建立索引反而会引入更多故障面。
//
// 分页用「时间游标」而不是偏移量：日志在不断追加，偏移量会漂移；
// 时间游标（BeforeMs）保证「加载更早」的语义稳定，前端据此做向上加载。
//
// 读取侧再次脱敏：即使写入侧已脱敏，磁盘上的文件仍可能被人工编辑或来自旧版本，
// 因此返回前统一走 Redact，保证前端永远拿不到未脱敏内容。
package logging

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 查询相关的默认值与上限（接口冻结清单 §8.6）。
const (
	// DefaultQueryLimit 是 LogQuery.Limit 缺省时的条数。
	DefaultQueryLimit = 200

	// MaxQueryLimit 是 LogQuery.Limit 的硬上限，超出部分在 Go 边界裁剪。
	//
	// 该值不信任前端传入：前端可以任意构造参数，必须在后端兜底，
	// 否则一次超大查询会读入整个日志目录并撑爆内存。
	MaxQueryLimit = 1000
)

// 查询读取时的扫描约束。
const (
	// maxScanLinesPerFile 是单次查询对单个文件最多扫描的行数。
	//
	// 取 20000：与 5 MiB 单文件上限对应的量级一致，足以覆盖一个完整会话；
	// 超出后停止扫描，避免异常大的单文件把一次查询拖成秒级停顿。
	maxScanLinesPerFile = 20000

	// maxEntriesInMemory 是单次查询在内存中保留的记录上限。
	//
	// 略大于 MaxQueryLimit，留出被过滤掉后的余量；防止在极端情况下无限累积。
	maxEntriesInMemory = MaxQueryLimit * 2
)

// QueryRequest 描述一次日志查询条件。
//
// 与接口冻结清单 §8.6 的 LogQuery 字段逐字一致：
// Level / Component / Limit / BeforeMs。
type QueryRequest struct {
	// Level 为级别过滤，取 debug / info / warn / error；空表示全部级别。
	Level string `json:"level"`

	// Component 为组件过滤，按完全匹配；空表示全部组件。
	Component string `json:"component"`

	// Limit 为本次返回的最大条数；<=0 时取 DefaultQueryLimit，超过 MaxQueryLimit 时被裁剪。
	Limit int `json:"limit"`

	// BeforeMs 为时间游标（UTC Unix 毫秒），只返回早于该时间的记录；0 表示不限。
	BeforeMs int64 `json:"beforeMs"`
}

// QueryResult 返回查询结果与下一页游标。
//
// NextBeforeMs 是「继续向上加载」时应当传入的 BeforeMs 值：
// 取本页最旧记录的毫秒时间戳；本页为空时为 0，表示已经到底。
type QueryResult struct {
	// Entries 为本页记录，按时间倒序（最新在前）。
	Entries []Record `json:"entries"`

	// NextBeforeMs 为下一页游标；0 表示没有更多记录。
	NextBeforeMs int64 `json:"nextBeforeMs"`
}

// normalizedLimit 返回裁剪后的查询条数。
//
// 规则：<=0 用默认值，>MaxQueryLimit 截到上限。该函数在 Go 边界执行，
// 前端无法绕过（接口冻结清单 §8.6 的强制要求）。
func (q QueryRequest) normalizedLimit() int {
	if q.Limit <= 0 {
		return DefaultQueryLimit
	}
	if q.Limit > MaxQueryLimit {
		return MaxQueryLimit
	}
	return q.Limit
}

// validLevelFilter 返回规范化后的级别过滤值；空表示不过滤，非法值按空处理。
func (q QueryRequest) validLevelFilter() Level {
	trimmed := strings.TrimSpace(q.Level)
	if trimmed == "" {
		return ""
	}
	if !Level(trimmed).IsValid() {
		// 非法级别不视为错误：按「不过滤」返回，避免前端传入脏值时整页报错。
		return ""
	}
	return ParseLevel(trimmed)
}

// Query 按条件读取进程级日志写入器所在目录的日志记录。
//
// 参数 logger 为进程级 Logger（通常为 Default）；为空或目录不可用时返回空结果。
// 该函数是包外唯一受支持的查询入口：告警目录由调用方无法伪造，
// 保证服务层不会因为传入任意路径而读取到不该读的内容。
func Query(logger *Logger, req QueryRequest) (QueryResult, error) {
	if logger == nil || logger.w == nil {
		return QueryResult{Entries: []Record{}}, nil
	}
	return queryLogs(logger.w.Directory(), req)
}

// queryLogs 按条件读取日志记录。
//
// 参数 dir 为日志目录（为空表示不可用，返回空结果而不是错误）；
// q 为查询条件。返回结果按时间倒序，最多 q.normalizedLimit() 条。
//
// 错误只在「目录存在但读取失败」时返回，由 LogService 转换为 LOG_IO_001。
func queryLogs(dir string, q QueryRequest) (QueryResult, error) {
	if dir == "" {
		// 目录不可写：没有文件可读，返回空结果（状态字段会提示用户原因）。
		return QueryResult{Entries: []Record{}}, nil
	}

	files, err := listLogFiles(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 目录尚未创建：等价于「暂无日志」，不作为错误。
			return QueryResult{Entries: []Record{}}, nil
		}
		return QueryResult{}, err
	}
	if len(files) == 0 {
		return QueryResult{Entries: []Record{}}, nil
	}

	// 从最新文件开始扫描：多数查询只需要最近几条，无需读完全部历史。
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime().After(files[j].ModTime())
	})

	limit := q.normalizedLimit()
	levelFilter := q.validLevelFilter()
	componentFilter := strings.TrimSpace(q.Component)

	collected := make([]Record, 0, limit)

	for _, info := range files {
		if len(collected) >= maxEntriesInMemory {
			break
		}

		entries, readErr := readFileEntries(filepath.Join(dir, info.Name()), q.BeforeMs, levelFilter, componentFilter)
		if readErr != nil {
			// 单文件读取失败时不整体失败：跳过该文件，继续读更早的文件，
			// 保证用户至少能看到部分可用日志。
			continue
		}

		collected = append(collected, entries...)

		// 已经收集到足够多的记录时提前停止（多留一些用于跨文件排序与裁剪）。
		if len(collected) >= limit {
			break
		}
	}

	// 统一按时间倒序排序：文件名顺序不等于记录时间顺序（跨天、时钟回拨都可能打乱）。
	sort.SliceStable(collected, func(i, j int) bool {
		return collected[i].timestampMillis() > collected[j].timestampMillis()
	})

	if len(collected) > limit {
		collected = collected[:limit]
	}

	result := QueryResult{Entries: collected, NextBeforeMs: 0}
	if len(collected) > 0 {
		result.NextBeforeMs = collected[len(collected)-1].timestampMillis()
	}
	return result, nil
}

// readFileEntries 读取单个日志文件并返回满足条件的记录。
//
// 参数 path 为文件路径；beforeMs 为时间上界（0 表示不限）；
// levelFilter 为空表示不过滤级别；componentFilter 为空表示不过滤组件。
// 返回的记录保持文件中的原始顺序（从旧到新），排序由调用方统一处理。
// 文件过大时最多扫描 maxScanLinesPerFile 行，避免单次查询被异常文件拖慢。
func readFileEntries(path string, beforeMs int64, levelFilter Level, componentFilter string) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	entries := make([]Record, 0, MaxQueryLimit)
	scanner := bufio.NewScanner(file)
	// 单条记录最大 1 MiB：日志行本身远小于该值，设置上限是为了防止畸形行撑爆缓冲。
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lines := 0
	for scanner.Scan() {
		lines++
		if lines > maxScanLinesPerFile {
			break
		}

		record, ok := decodeRecord(scanner.Bytes())
		if !ok {
			// 无法解析的行（例如 P0 的纯文本 launcher.log 内容）直接跳过，
			// 不让格式不兼容的历史内容影响结构化查询。
			continue
		}

		if beforeMs > 0 && record.timestampMillis() >= beforeMs {
			continue
		}
		if levelFilter != "" && record.Level != string(levelFilter) {
			continue
		}
		if componentFilter != "" && record.Component != componentFilter {
			continue
		}

		entries = append(entries, record)
	}

	if err := scanner.Err(); err != nil {
		return entries, err
	}
	return entries, nil
}

// decodeRecord 把一行 JSON 解析为已脱敏的 Record。
//
// 解析成功后会再执行一次脱敏：磁盘内容可能被手工编辑或来自旧版本，
// 读取侧不能假设写入侧已经处理干净。
func decodeRecord(line []byte) (Record, bool) {
	var record Record
	if err := json.Unmarshal(line, &record); err != nil {
		return Record{}, false
	}

	// 时间戳是必需字段：缺失即为无法排序的记录，直接丢弃。
	if strings.TrimSpace(record.Timestamp) == "" {
		return Record{}, false
	}

	// 级别必须是四个合法值之一，否则丢弃（避免前端筛选出现未知档位）。
	if !Level(record.Level).IsValid() {
		return Record{}, false
	}

	// 读取侧统一再脱敏一遍，保证返回给前端的内容永远不含敏感信息。
	record.Message = Redact(record.Message)
	record.Event = Redact(record.Event)
	record.CorrelationID = Redact(record.CorrelationID)
	record.OperationID = Redact(record.OperationID)
	record.ErrorCode = Redact(record.ErrorCode)
	record.Attributes = RedactAttributes(record.Attributes)

	return record, true
}

// timestampMillis 返回记录的 UTC Unix 毫秒时间戳。
//
// 解析失败返回 0：这类记录会被排到最末，不影响正常记录的展示顺序。
func (r Record) timestampMillis() int64 {
	parsed, err := time.Parse(entryTimestampLayout, r.Timestamp)
	if err != nil {
		// 兼容带 Z 与带偏移的两种 RFC3339 写法：写入侧固定用 Z，
		// 但历史文件可能来自使用 +00:00 的格式。
		parsed, err = time.Parse(time.RFC3339Nano, r.Timestamp)
		if err != nil {
			return 0
		}
	}
	return parsed.UnixMilli()
}

// TimestampMillis 返回记录的 UTC Unix 毫秒时间戳，供服务层转换为前端字段。
//
// 解析失败返回 0：这类记录排到最末，不影响正常记录的展示顺序。
func (r Record) TimestampMillis() int64 {
	return r.timestampMillis()
}
