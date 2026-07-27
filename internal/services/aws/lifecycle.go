// Package aws contains shared AWS API client wrappers used by the operator. Today this is limited
// to the Auto Scaling lifecycle-hook calls used to hold an EC2 instance in Terminating:Wait until
// its WEKA drive can be safely released (see internal/controllers/wekacontainer/funcs_aws_termination_lifecycle.go).
package aws

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/pkg/errors"
)

// LifecycleClient abstracts the AWS Auto Scaling lifecycle-hook calls needed to hold/release an
// EC2 instance at Terminating:Wait. Kept as an interface so callers can be unit-tested with a fake.
type LifecycleClient interface {
	// DescribeInstance resolves the ASG name and current lifecycle state of instanceID.
	DescribeInstance(ctx context.Context, instanceID string) (asgName string, lifecycleState string, err error)
	// RecordHeartbeat extends the lifecycle hook's HeartbeatTimeout, keeping the instance held.
	RecordHeartbeat(ctx context.Context, hookName, asgName, instanceID string) error
	// CompleteAction resolves the lifecycle action (e.g. with result "CONTINUE"), letting the ASG
	// proceed with the instance's termination. Idempotent: completing an already-resolved action is
	// treated as success.
	CompleteAction(ctx context.Context, hookName, asgName, instanceID, result string) error
	// PutTerminationHook creates or updates an EC2_INSTANCE_TERMINATING lifecycle hook named hookName on asgName
	// with the given HeartbeatTimeout and DefaultResult=CONTINUE (no notification target/role).
	PutTerminationHook(ctx context.Context, asgName, hookName string, heartbeatTimeout int32) error
}

// realLifecycleClient is the real AWS-backed implementation of LifecycleClient.
type realLifecycleClient struct {
	region string
	client *autoscaling.Client
}

// NewLifecycleClient builds a LifecycleClient bound to the given AWS region. The underlying AWS
// client and credentials are resolved lazily on first use.
func NewLifecycleClient(region string) LifecycleClient {
	return &realLifecycleClient{region: region}
}

func (a *realLifecycleClient) ensureClient(ctx context.Context) (*autoscaling.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if a.region != "" {
		opts = append(opts, awsconfig.WithRegion(a.region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load AWS config")
	}
	a.client = autoscaling.NewFromConfig(cfg)
	return a.client, nil
}

func (a *realLifecycleClient) DescribeInstance(ctx context.Context, instanceID string) (asgName, lifecycleState string, err error) {
	c, err := a.ensureClient(ctx)
	if err != nil {
		return "", "", err
	}
	out, err := c.DescribeAutoScalingInstances(ctx, &autoscaling.DescribeAutoScalingInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return "", "", errors.Wrap(err, "DescribeAutoScalingInstances failed")
	}
	if len(out.AutoScalingInstances) == 0 {
		return "", "", errors.Errorf("no ASG instance found for instance-id %s", instanceID)
	}
	inst := out.AutoScalingInstances[0]
	return aws.ToString(inst.AutoScalingGroupName), aws.ToString(inst.LifecycleState), nil
}

func (a *realLifecycleClient) RecordHeartbeat(ctx context.Context, hookName, asgName, instanceID string) error {
	c, err := a.ensureClient(ctx)
	if err != nil {
		return err
	}
	_, err = c.RecordLifecycleActionHeartbeat(ctx, &autoscaling.RecordLifecycleActionHeartbeatInput{
		LifecycleHookName:    aws.String(hookName),
		AutoScalingGroupName: aws.String(asgName),
		InstanceId:           aws.String(instanceID),
	})
	return errors.Wrap(err, "RecordLifecycleActionHeartbeat failed")
}

func (a *realLifecycleClient) CompleteAction(ctx context.Context, hookName, asgName, instanceID, result string) error {
	c, err := a.ensureClient(ctx)
	if err != nil {
		return err
	}
	_, err = c.CompleteLifecycleAction(ctx, &autoscaling.CompleteLifecycleActionInput{
		LifecycleHookName:     aws.String(hookName),
		AutoScalingGroupName:  aws.String(asgName),
		InstanceId:            aws.String(instanceID),
		LifecycleActionResult: aws.String(result),
	})
	if err != nil && strings.Contains(err.Error(), "No active Lifecycle Action found") {
		// Idempotent: our hook was already resolved (completed earlier, or it timed out). Not an error.
		return nil
	}
	return errors.Wrap(err, "CompleteLifecycleAction failed")
}

func (a *realLifecycleClient) PutTerminationHook(ctx context.Context, asgName, hookName string, heartbeatTimeout int32) error {
	c, err := a.ensureClient(ctx)
	if err != nil {
		return err
	}
	_, err = c.PutLifecycleHook(ctx, &autoscaling.PutLifecycleHookInput{
		AutoScalingGroupName: aws.String(asgName),
		LifecycleHookName:    aws.String(hookName),
		LifecycleTransition:  aws.String("autoscaling:EC2_INSTANCE_TERMINATING"),
		HeartbeatTimeout:     aws.Int32(heartbeatTimeout),
		DefaultResult:        aws.String("CONTINUE"),
	})
	return errors.Wrap(err, "PutLifecycleHook failed")
}
