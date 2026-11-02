package download

// planAction controls the children of an aggregate task; the parent is never dispatched.
func (q *Queue) planAction(id, action string) (DownloadTaskView, *Error, bool) {
	q.mu.Lock()
	parent, exists := q.tasks[id]
	if !exists || parent.TaskType != TaskTypePlan {
		q.mu.Unlock()
		return DownloadTaskView{}, nil, false
	}
	if IsTerminal(parent.State) {
		q.mu.Unlock()
		return DownloadTaskView{}, newInputError(ErrorCodeState, messageStateNotAllowed), true
	}
	target := StatePaused
	switch action {
	case "pause":
		if parent.State == StatePaused {
			q.mu.Unlock()
			return DownloadTaskView{}, newInputError(ErrorCodeState, messageStateNotAllowed), true
		}
	case "resume":
		if parent.State != StatePaused {
			q.mu.Unlock()
			return DownloadTaskView{}, newInputError(ErrorCodeState, messageStateNotAllowed), true
		}
		target = StateQueued
	case "cancel":
		target = StateCancelled
		for _, child := range q.tasks {
			if child.ParentTaskID == id && child.State == StateCommitting {
				q.mu.Unlock()
				return DownloadTaskView{}, newInputError(ErrorCodeCancel, messageCancelDuringCommit), true
			}
		}
	}
	changed := []Task{}
	for _, child := range q.tasks {
		if child.ParentTaskID != id || IsTerminal(child.State) {
			continue
		}
		eligible := false
		switch action {
		case "pause":
			eligible = child.State == StateQueued || child.State == StateDownloading
		case "resume":
			eligible = child.State == StatePaused
		case "cancel":
			eligible = CanTransit(child.State, StateCancelled)
		}
		if !eligible {
			continue
		}
		child.State = target
		child.UpdatedAtMs = q.now()
		if target == StateCancelled {
			child.CompletedAtMs = child.UpdatedAtMs
		}
		if target == StateQueued {
			q.enqueuePendingLocked(child.ID)
		}
		changed = append(changed, *child)
	}
	parent.State = target
	parent.UpdatedAtMs = q.now()
	if target == StateCancelled {
		parent.CompletedAtMs = parent.UpdatedAtMs
	}
	snapshot := *parent
	q.ownedPlans[id] = true
	q.mu.Unlock()
	for _, child := range changed {
		q.persist(child)
		q.emit(child)
		if action != "resume" {
			q.stopWorker(child.ID)
		}
	}
	q.persist(snapshot)
	q.emit(snapshot)
	if action == "resume" {
		q.kickLoop()
	}
	view, err := q.viewOf(id)
	return view, err, true
}
