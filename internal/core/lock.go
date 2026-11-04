package core

import (
	"sync"
)

// 锁的分层说明（顺序不可颠倒）：
//
//	Global → Identity → Version → Artifact
//
// 任何反向获取都视为 bug，会造成死锁或让「重建与安装互斥」失效。
//
// 为什么需要四层而不是一把大锁：安装不同加载器组合时，彼此的产物没有交集
// （libraries 由内容决定、versions 是各自目录），若全部串行化，Identity 层
// 就失去意义。分层后只有真正冲突的操作才互斥。
//
// 为什么 Global 是读写锁：安装与删除都要阻止「重建」同时进行，但它们彼此
// 可以并行，因此持有 Global 的**读**锁；重建持有 Global 的**写**锁。

// Locks 是核心资源的锁管理器。
//
// 零值不可用，必须经由 NewLocks 创建。
type Locks struct {
	global   sync.RWMutex
	mu       sync.Mutex
	identity map[string]*keyedLock
	version  map[string]*keyedLock
	artifact map[string]*keyedLock
}

// keyedLock 是带使用计数的按名互斥锁。
//
// 计数归零后从表中移除，避免长期运行后表无限增长。
type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// NewLocks 创建锁管理器。
func NewLocks() *Locks {
	return &Locks{
		identity: map[string]*keyedLock{},
		version:  map[string]*keyedLock{},
		artifact: map[string]*keyedLock{},
	}
}

// acquire 取得一张按名互斥锁，返回解锁函数。
func (l *Locks) acquire(table map[string]*keyedLock, key string) func() {
	l.mu.Lock()
	entry, ok := table[key]
	if !ok {
		entry = &keyedLock{}
		table[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()

		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(table, key)
		}
		l.mu.Unlock()
	}
}

// GlobalRead 取得全局读锁，返回解锁函数。
//
// 安装、修复、删除、版本级回收都必须持有它；重建持 GlobalWrite。
func (l *Locks) GlobalRead() func() {
	l.global.RLock()
	return l.global.RUnlock
}

// GlobalWrite 取得全局写锁，返回解锁函数。
//
// 用于核心重建：它会替换整个核心目录，期间不允许任何安装、修复、删除与
// 启动准备。
func (l *Locks) GlobalWrite() func() {
	l.global.Lock()
	return l.global.Unlock
}

// Identity 取得某安装身份的互斥锁。
//
// 同一身份重复安装会被串行化；不同身份互不影响。
func (l *Locks) Identity(identity string) func() {
	return l.acquire(l.identity, identity)
}

// Version 取得某版本目录的互斥锁。
//
// 保护「安装 / 修复 / 删除 / 租约登记」对同一版本目录的并发访问。
func (l *Locks) Version(versionID string) func() {
	return l.acquire(l.version, versionID)
}

// Artifact 取得某共享文件路径的互斥锁。
//
// 只用于写入 libraries/ 与 assets/ 下的文件：两个不同版本的安装可能同时
// 需要同一个 JAR，内容一致时安全共用，但仍需避免同时写入。
func (l *Locks) Artifact(relPath string) func() {
	return l.acquire(l.artifact, relPath)
}
