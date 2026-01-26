package wekacontainer

import "fmt"

type NoWekaFsDriverFound struct{}

func (e *NoWekaFsDriverFound) Error() string {
	return "No wekafs driver found"
}

type NodeAgentPodNotRunning struct{}

func (e *NodeAgentPodNotRunning) Error() string {
	return "Agent pod exists but is not running"
}

type NodeAgentImageMismatch struct {
	NodeAgentImage string
	OperatorImage  string
}

func (e *NodeAgentImageMismatch) Error() string {
	return fmt.Sprintf("node agent image mismatch: node-agent has %s but operator has %s",
		e.NodeAgentImage, e.OperatorImage)
}
