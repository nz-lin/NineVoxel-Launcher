package instance

// CleanupUnusedCoreForUser 回收无人引用的核心版本。
//
// 供设置页的「清理未使用核心」入口调用。返回统计而不是静默执行：用户点这个
// 按钮就是为了知道「到底释放了多少、为什么有些没删」。
func (s *InstanceService) CleanupUnusedCoreForUser() (ReclaimReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return ReclaimReport{}, err
	}
	if err := s.reconcileLocked(); err != nil {
		return ReclaimReport{}, err
	}
	// 全程持锁：回收要读实例清单判断引用，与实例创建/删除并发会得到
	// 「刚建的实例还没写完清单」这类中间状态。
	return s.CleanupUnusedCore()
}
