package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type BlissRunError struct {
	Err     error
	Message string
}

func (e *BlissRunError) Error() string {
	return fmt.Sprintf("Jobless run error - %s: %v", e.Message, e.Err)
}

type DeploymentError struct {
	Err       error
	Arguments []string
}

func (e *DeploymentError) Error() string {
	return fmt.Sprintf("Deployment error - %v: %v", e.Arguments, e.Err)
}

type InstallationError struct {
	Err       error
	Arguments []string
}

func (e *InstallationError) Error() string {
	return fmt.Sprintf("Installation error - %v: %v", e.Arguments, e.Err)
}

func NewJobless(ctx context.Context, version string) Jobless {
	return &joblessGoRun{
		KubeConfig:   make(map[string]string),
		BlissVersion: version,
	}
}

type (
	joblessGoRun struct {
		KubeConfig   map[string]string
		BlissVersion string
	}
)

func (j *joblessGoRun) EnsureDeployment(ctx context.Context, params ProvisionParams) (*Deployment, error) {
	template := params.GetTemplate()
	subnetID := params.GetSubnetID()
	region := params.GetRegion()
	securityGroups := params.GetSecurityGroups()
	amiID := params.GetAmiID()
	keyPairName := params.GetKeyPairName()
	clusterName := params.GetClusterName()

	// Skip if deployment already exists
	deploymentBytes, err := j.GoRunBliss("info", "--cluster-name", clusterName)
	if err == nil {
		deployment, err := unmarshal[Deployment](deploymentBytes)
		if err != nil {
			return nil, err
		}
		return deployment, nil
	}

	args := []string{"provision"}
	args = append(args, "--template", template)
	args = append(args, "--subnet-id", subnetID)
	args = append(args, "--region", region)
	for _, sg := range securityGroups {
		args = append(args, "--security-groups", sg)
	}
	args = append(args, "--ami-id", amiID)
	args = append(args, "--key-pair-name", keyPairName)
	args = append(args, "--cluster-name", clusterName)

	if params.IsDualStack() {
		args = append(args, "--dual-stack")
	}

	_, err = j.GoRunBliss(args...)
	if err != nil {
		return nil, &DeploymentError{
			Err:       err,
			Arguments: args,
		}
	}

	deploymentBytes, err = j.GoRunBliss("info", "--cluster-name", clusterName)
	if err != nil {
		return nil, &DeploymentError{
			Err:       err,
			Arguments: args,
		}
	}
	deployment, err := unmarshal[Deployment](deploymentBytes)
	if err != nil {
		return nil, err
	}

	return deployment, nil
}

func (j *joblessGoRun) DeleteDeployment(ctx context.Context, clusterName string) error {
	_, err := j.GoRunBliss("delete", clusterName)
	if err != nil {
		return &DeploymentError{
			Err:       err,
			Arguments: []string{"delete", "--cluster-name", clusterName},
		}
	}

	return nil
}

func (j *joblessGoRun) Install(ctx context.Context, params InstallParams) (*Installation, error) {
	clusterName := params.GetClusterName()
	quayUsername := params.GetQuayUsername()
	quayPassword := params.GetQuayPassword()
	wekaImage := params.GetWekaImage()
	hasCsi := params.HasCsi()

	args := []string{"install"}
	args = append(args, "--cluster-name", clusterName)
	args = append(args, "--quay-username", quayUsername)
	args = append(args, "--quay-password", quayPassword)
	args = append(args, "--weka-image", wekaImage)
	if !hasCsi {
		args = append(args, "--no-csi")
	}

	installationBytes, err := j.GoRunBliss(args...)
	if err != nil {
		return nil, &InstallationError{
			Err:       err,
			Arguments: args,
		}
	}
	installation, err := unmarshal[Installation](installationBytes)
	if err != nil {
		return nil, err
	}
	return installation, nil
}

func (j *joblessGoRun) GetKubeConfig(clusterName string) (string, error) {
	kubeconfig, ok := j.KubeConfig[clusterName]
	if !ok {
		return "", fmt.Errorf("kubeconfig not found for cluster %s", clusterName)
	}
	return kubeconfig, nil
}

func (j *joblessGoRun) GoRunBliss(args ...string) ([]byte, error) {
	return j.GoRunBlissRemote(args...)
	// return GoRunBlissDev[T](args...)
}

// GoRunBlissRemote runs bliss using the latest version of the jobless module
// This uses the go module system to fetch the latest version of the jobless module
func (j *joblessGoRun) GoRunBlissRemote(args ...string) ([]byte, error) {
	blissVersion := "v1.11.1"
	os.Setenv("GOPRIVATE", "github.com/weka")
	module := fmt.Sprintf("github.com/weka/bliss@%s", blissVersion)

	// Run go run jobless/main.go
	os.Setenv("AWS_REGION", "eu-west-1")
	// os.Setenv("AWS_PROFILE", "devkube")

	// Suppress debug logs, they interfere with the JSON parsing
	os.Setenv("LOG_LEVEL", "2")

	runArgs := append([]string{"run", module, "--json"}, args...)
	cmd := exec.Command("go", runArgs...)

	outputBytes, err := cmd.Output()
	if err != nil {
		var message string
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message = fmt.Sprintf("failed to run bliss: %s", string(exitErr.Stderr))
		} else {
			message = "failed to run bliss"
		}
		return nil, &BlissRunError{
			Err:     err,
			Message: message,
		}
	}

	if len(outputBytes) == 0 {
		return nil, nil
	}

	return outputBytes, nil
}

// GoRunBlissDev runs bliss using the local version of jobless
// Jobless must be checked out into a directory next to the operator
// (ie src/github.com/weka/jobless and src/github.com/weka/weka-operator)
func GoRunBlissDev[T any](args ...string) (*T, error) {
	pathToMainGo, workingDir, err := localGoRun()
	if err != nil {
		return nil, err
	}

	// Run go run jobless/main.go
	os.Setenv("AWS_REGION", "eu-west-1")
	// os.Setenv("AWS_PROFILE", "devkube")

	// Suppress debug logs, they interfere with the JSON parsing
	os.Setenv("LOG_LEVEL", "2")

	runArgs := append([]string{"run", pathToMainGo, "--json"}, args...)
	cmd := exec.Command("go", runArgs...)
	cmd.Dir = workingDir

	outputBytes, err := cmd.Output()
	if err != nil {
		var message string
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message = fmt.Sprintf("failed to run bliss: %s", string(exitErr.Stderr))
		} else {
			message = "failed to run bliss"
		}
		return nil, &BlissRunError{
			Err:     err,
			Message: message,
		}
	}

	if len(outputBytes) == 0 {
		return nil, nil
	}

	result := new(T)
	if err := json.Unmarshal(outputBytes, result); err != nil {
		return nil, &BlissRunError{
			Err:     err,
			Message: fmt.Sprintf("failed to unmarshal output: %s", string(outputBytes)),
		}
	}
	return result, nil
}

func localGoRun() (string, string, error) {
	// Find bliss/main.go relative to current file
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", &BlissRunError{
			Err:     err,
			Message: "failed to get current working directory",
		}
	}

	workingDir := filepath.Join(cwd, "..", "..", "..", "jobless")
	workingDir, err = filepath.Abs(workingDir)
	if err != nil {
		return "", "", &BlissRunError{
			Err:     err,
			Message: "failed to get absolute path to jobless",
		}
	}

	pathToMainGo := filepath.Join(workingDir, "main.go")
	pathToMainGo, err = filepath.Abs(pathToMainGo)
	if err != nil {
		return "", "", &BlissRunError{
			Err:     err,
			Message: "failed to get absolute path to bliss/main.go",
		}
	}

	_, err = os.Stat(pathToMainGo)
	if err != nil {
		return "", "", &BlissRunError{
			Err:     err,
			Message: "bliss/main.go not found",
		}
	}

	return pathToMainGo, workingDir, nil
}

func unmarshal[T any](bytes []byte) (*T, error) {
	result := new(T)
	if err := json.Unmarshal(bytes, result); err != nil {
		return nil, &BlissRunError{
			Err:     err,
			Message: fmt.Sprintf("failed to unmarshal output: %s", string(bytes)),
		}
	}
	return result, nil
}
