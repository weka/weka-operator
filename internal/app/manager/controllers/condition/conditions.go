package condition

import (
	"github.com/kr/pretty"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	CondPodsCreated                 = "PodsCreated"
	CondClusterClientSecretsCreated = "ClusterClientsSecretsCreated"
	CondClusterClientSecretsApplied = "ClusterClientsSecretsApplied"
	CondDefaultFsCreated            = "CondDefaultFsCreated"
	CondS3ClusterCreated            = "CondS3ClusterCreated" // not pre-set on purpose, as optional
	CondPodsReady                   = "PodsReady"
	CondClusterCreated              = "ClusterCreated"
	CondDrivesAdded                 = "DrivesAdded"
	CondIoStarted                   = "IoStarted"
	CondJoinedCluster               = "JoinedCluster"
	CondJoinedS3Cluster             = "JoinedS3Cluster"
	CondEnsureDrivers               = "EnsuredDrivers"
)

type UpdateConditionFunc func(c WekaClusterCondition) WekaClusterCondition

type WekaClusterCondition interface {
	RecordIn(conditions *[]metav1.Condition)
	FindIn(conditions []metav1.Condition) (*metav1.Condition, error)
	GetType() string
	SetFailed(err error) *condition
	SetStatus(status metav1.ConditionStatus)
	SetReason(reason string)
	SetMessage(message string)
}

type condition struct {
	metav1.Condition
}

func (c *condition) String() string {
	return c.Type
}

func (c *condition) GetType() string {
	return c.Type
}

func (c *condition) FindIn(conditions []metav1.Condition) (*metav1.Condition, error) {
	condition := meta.FindStatusCondition(conditions, c.Type)
	if condition == nil {
		return nil, pretty.Errorf("condition not found", "condition", c.String())
	}
	return condition, nil
}

func (c *condition) RecordIn(conditions *[]metav1.Condition) {
	meta.SetStatusCondition(conditions, c.Condition)
}

func (c *condition) SetFailed(err error) *condition {
	c.Status = metav1.ConditionFalse
	c.Reason = "Failed"
	c.Message = err.Error()
	return c
}

func (c *condition) SetStatus(status metav1.ConditionStatus) {
	c.Status = status
}

func (c *condition) SetReason(reason string) {
	c.Reason = reason
}

func (c *condition) SetMessage(message string) {
	c.Message = message
}

func AllCreated(c WekaClusterCondition) WekaClusterCondition {
	c.SetStatus(metav1.ConditionTrue)
	c.SetReason("Init")
	c.SetMessage("All pods are created")
	return c
}

func CondClusterSecretsCreated() *condition {
	return &condition{
		Condition: metav1.Condition{
			Type:    "ClusterSecretsCreated",
			Status:  metav1.ConditionFalse,
			Reason:  "Init",
			Message: "Secrets are not created yet",
		},
	}
}

// CondClusterSecretsApplied
func CondClusterSecretsApplied() *condition {
	return &condition{
		Condition: metav1.Condition{
			Type:    "ClusterSecretsApplied",
			Status:  metav1.ConditionFalse,
			Reason:  "Init",
			Message: "Secrets are not applied yet",
		},
	}
}

func NewCondPodsReady() *condition {
	return &condition{
		Condition: metav1.Condition{
			Type:    CondPodsReady,
			Status:  metav1.ConditionFalse,
			Reason:  "Init",
			Message: "The pods for the custom resource are not ready yet",
		},
	}
}

func ReadyToClusterize(c WekaClusterCondition) WekaClusterCondition {
	if c.GetType() != CondPodsReady {
		panic("ReadyToClusterize can only be called on a PodsReady condition")
	}
	c.SetStatus(metav1.ConditionTrue)
	c.SetReason("Init")
	c.SetMessage("All weka containers are ready for clusterization")
	return c
}

func SetFailed(err error) func(c WekaClusterCondition) WekaClusterCondition {
	return func(c WekaClusterCondition) WekaClusterCondition {
		c.SetFailed(err)
		return c
	}
}
