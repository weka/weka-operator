# AWS EKS — operator IAM for safe scale-down

On EKS, when an Auto Scaling group (ASG) scales in a backend node, the EC2 instance is terminated
before WEKA can shut its backend pods down gracefully — a drive before its data is rebuilt off it, or
a compute/protocol process mid-drain. To prevent this, the operator manages an
`EC2_INSTANCE_TERMINATING` lifecycle hook (`weka-drive-drain`) on each backend node's ASG and holds
the terminating instance in `Terminating:Wait` until the node's backend pods have exited gracefully
(their data has been rebuilt/replicated and they have shut down cleanly). Only then does it let the
ASG proceed with termination.

For this to work the operator manager pod must be able to call the AWS Auto Scaling APIs. **Without
these permissions the operator cannot delay termination, and a scale-in can destroy an instance
mid-rebuild — exactly the data loss this feature prevents.** On initial cluster provisioning the
operator fails closed (it will not form an unprotected cluster if it cannot create the hook); on an
already-running cluster it fails open and emits a `NoAwsTerminationLifecycleHook` Warning event on the
WekaCluster.

## Required permissions

The operator manager pod needs the following IAM policy:

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": [
                "autoscaling:DescribeAutoScalingInstances",
                "autoscaling:DescribeAutoScalingGroups",
                "autoscaling:RecordLifecycleActionHeartbeat",
                "autoscaling:CompleteLifecycleAction",
                "autoscaling:PutLifecycleHook"
            ],
            "Resource": "*"
        }
    ]
}
```

| Action | Why the operator needs it |
| --- | --- |
| `DescribeAutoScalingInstances` / `DescribeAutoScalingGroups` | Resolve a node's ASG and read whether an instance is currently held (`Terminating:Wait`). |
| `PutLifecycleHook` | Create/maintain the `weka-drive-drain` hook on each backend node's ASG. |
| `RecordLifecycleActionHeartbeat` | Extend the hold (heartbeat) while backend pods are still draining. |
| `CompleteLifecycleAction` | Release the hold once the node's backend pods have exited gracefully, letting the ASG terminate the instance. |

There are two ways to grant these to the operator pod. Pick **one**.

## Option A — node group instance role (simplest)

Attach the policy above to the IAM role used by the backend node group. Permissions granted to the
node's instance role are available to pods running on that node (via the instance metadata service),
so the operator pod inherits them wherever it is scheduled.

This is the least amount of setup, but it is coarse-grained: **every** pod on those nodes gets the
Auto Scaling permissions, not just the operator. If you want the permissions scoped to the operator's
service account only, use Option B.

## Option B — EKS Pod Identity (recommended, scoped to the operator)

[EKS Pod Identity](https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html) binds an IAM
role to a specific Kubernetes service account, so only the operator's pods receive the permissions.

The examples assume the default Helm install (`prefix: weka-operator`), i.e. namespace
`weka-operator-system` and service account `weka-operator-controller-manager`. Adjust if you deploy
with a different `prefix`/namespace.

### 1. Install the Pod Identity agent add-on

```bash
aws eks create-addon \
  --region eu-west-1 \
  --cluster-name yaron-eks \
  --addon-name eks-pod-identity-agent
```

### 2. Create the role and attach the policy

Create a role the EKS Pod Identity service can assume:

```bash
aws iam create-role --role-name weka-operator-role \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"pods.eks.amazonaws.com"},"Action":["sts:AssumeRole","sts:TagSession"]}]}'
```

Attach the Auto Scaling policy:

```bash
aws iam put-role-policy --role-name weka-operator-role --policy-name lifecycle-hook \
  --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":[
    "autoscaling:DescribeAutoScalingInstances","autoscaling:DescribeAutoScalingGroups",
    "autoscaling:RecordLifecycleActionHeartbeat","autoscaling:CompleteLifecycleAction",
    "autoscaling:PutLifecycleHook"],"Resource":"*"}]}'
```

### 3. Associate the role with the operator's service account

```bash
REGION=$(kubectl get nodes -o jsonpath='{.items[0].spec.providerID}' | sed -E 's#aws:///([a-z-]+[0-9])[a-z].*#\1#')
CLUSTER=$(kubectl config current-context | sed -E 's#.*cluster/##')   # strips ARN prefix if present
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)

aws eks create-pod-identity-association --region "$REGION" --cluster-name "$CLUSTER" \
  --namespace weka-operator-system \
  --service-account weka-operator-controller-manager \
  --role-arn "arn:aws:iam::${ACCOUNT}:role/weka-operator-role"
```

Restart the operator pod (or wait for it to be rescheduled) so the association takes effect, then
provision or reconcile a WekaCluster. The operator will create the `weka-drive-drain` hook on the
backend ASGs and, on scale-in, hold each instance until its data has been safely rebuilt.

## Verifying

- The hook exists on a backend node's ASG:
  ```bash
  aws autoscaling describe-lifecycle-hooks --auto-scaling-group-name <asg> \
    --query "LifecycleHooks[?LifecycleHookName=='weka-drive-drain']"
  ```
- On a missing/denied permission the WekaCluster shows a `NoAwsTerminationLifecycleHook` Warning
  event (`kubectl describe wekacluster <name>`); the operator logs the full AWS error.
- Non-AWS clusters (bare-metal / OCI) are a no-op and need none of this.
