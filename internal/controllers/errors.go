package controllers

type NoWekaFsDriverFound struct{}

func (e *NoWekaFsDriverFound) Error() string {
	return "No wekafs driver found"
}

type NodeAgentPodNotRunning struct{}

func (e *NodeAgentPodNotRunning) Error() string {
	return "Agent pod exists but is not running"
}
