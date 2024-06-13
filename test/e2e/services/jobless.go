package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/weka/weka-operator/internal/pkg/instrumentation"
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
	if template == "" {
		return nil, fmt.Errorf("template is not set")
	}
	subnetID := params.GetSubnetID()
	if subnetID == "" {
		return nil, fmt.Errorf("subnet ID is not set")
	}
	region := params.GetRegion()
	if region == "" {
		return nil, fmt.Errorf("region is not set")
	}
	clusterName := params.GetClusterName()
	if clusterName == "" {
		return nil, fmt.Errorf("cluster name is not set")
	}
	securityGroups := params.GetSecurityGroups()
	if len(securityGroups) == 0 {
		return nil, fmt.Errorf("security groups are not set")
	}
	amiID := params.GetAmiID()
	if amiID == "" {
		return nil, fmt.Errorf("AMI ID is not set")
	}
	keyPairName := params.GetKeyPairName()
	if keyPairName == "" {
		return nil, fmt.Errorf("key pair name is not set")
	}

	// Skip if deployment already exists
	deployment, err := j.RunInfo(ctx, clusterName)
	if err == nil {
		return deployment, nil
	}

	if err := j.RunProvision(ctx, params); err != nil {
		return nil, err
	}

	deployment, err = j.RunInfo(ctx, clusterName)
	if err != nil {
		return nil, err
	}
	return deployment, nil
}

func (j *joblessGoRun) DeleteDeployment(ctx context.Context, clusterName string) error {
	return j.GoRunBliss(ctx, "delete", clusterName)
}

func (j *joblessGoRun) Install(ctx context.Context, params InstallParams) (*Installation, error) {
	clusterName := params.GetClusterName()
	quayUsername := params.GetQuayUsername()
	quayPassword := params.GetQuayPassword()
	wekaImage := params.GetWekaImage()
	hasCsi := params.HasCsi()
	operatorVersion := params.GetOperatorVersion()

	args := []string{"install"}
	args = append(args, "--cluster-name", clusterName)
	args = append(args, "--quay-username", quayUsername)
	args = append(args, "--quay-password", quayPassword)
	args = append(args, "--weka-image", wekaImage)
	args = append(args, "--no-weka-client")

	if operatorVersion == "" {
		args = append(args, "--no-operator")
		args = append(args, "--no-weka-cluster")
	} else {
		args = append(args, "--operator-version", operatorVersion)
	}
	if !hasCsi {
		args = append(args, "--no-csi")
	}

	installationBytes, err := j.GoRunBlissWithJsonOutput(ctx, args...)
	if err != nil {
		return nil, &InstallationError{
			Err:       err,
			Arguments: args,
		}
	}
	if len(installationBytes) == 0 {
		return nil, fmt.Errorf("installation bytes are empty")
	}
	installation, err := unmarshal[Installation](installationBytes)
	if err != nil {
		return nil, err
	}
	if installation == nil {
		return nil, fmt.Errorf("unmarshalled installation is nil")
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

func (j *joblessGoRun) RunInfo(ctx context.Context, clusterName string) (*Deployment, error) {
	deploymentBytes, err := j.GoRunBlissWithJsonOutput(ctx, "info", "--cluster-name", clusterName)
	if err != nil {
		return nil, err
	}
	return unmarshal[Deployment](deploymentBytes)
}

func (j *joblessGoRun) RunProvision(ctx context.Context, params ProvisionParams) error {
	args := []string{"provision"}
	args = append(args, "--template", params.GetTemplate())
	args = append(args, "--subnet-id", params.GetSubnetID())
	args = append(args, "--region", params.GetRegion())
	for _, sg := range params.GetSecurityGroups() {
		args = append(args, "--security-groups", sg)
	}
	args = append(args, "--ami-id", params.GetAmiID())
	args = append(args, "--key-pair-name", params.GetKeyPairName())
	args = append(args, "--cluster-name", params.GetClusterName())

	if params.IsDualStack() {
		args = append(args, "--dual-stack")
	}

	return j.GoRunBliss(ctx, args...)
}

func (j *joblessGoRun) GoRunBliss(ctx context.Context, args ...string) error {
	_, _, done := instrumentation.GetLogSpan(ctx, "GoRunBliss")
	defer done()

	stdOutReader := func(stdOutPipe io.Reader) error {
		go func() {
			// Stream STDOUT
			stdoutScanner := bufio.NewScanner(stdOutPipe)
			for stdoutScanner.Scan() {
				// logger.Info(stdoutScanner.Text())
				fmt.Println(stdoutScanner.Text())
			}
			if err := stdoutScanner.Err(); err != nil {
				err = &BlissRunError{
					Err:     err,
					Message: "failed to read stdout",
				}
			}
		}()
		return nil
	}
	return j.GoRunBlissRemote(ctx, stdOutReader, args...)
	// return GoRunBlissDev[T](args...)
}

func (j *joblessGoRun) GoRunBlissWithJsonOutput(ctx context.Context, args ...string) ([]byte, error) {
	// Suppress debug logs, they interfere with the JSON parsing
	oldLogLevel := os.Getenv("LOG_LEVEL")
	os.Setenv("LOG_LEVEL", "2")
	defer os.Setenv("LOG_LEVEL", oldLogLevel)

	args = append(args, "--json")
	outputBytes := []byte{}
	stdOutReader := func(stdOutPipe io.Reader) error {
		// Synchronously read STDOUT
		stdoutScanner := bufio.NewScanner(stdOutPipe)
		for stdoutScanner.Scan() {
			line := stdoutScanner.Bytes()
			outputBytes = append(outputBytes, line...)
		}
		if err := stdoutScanner.Err(); err != nil {
			return &BlissRunError{
				Err:     err,
				Message: "failed to read stdout",
			}
		}
		return nil
	}
	if err := j.GoRunBlissRemote(ctx, stdOutReader, args...); err != nil {
		return nil, err
	}
	return outputBytes, nil
	// return GoRunBlissDev[T](args...)
}

// GoRunBlissRemote runs bliss using the latest version of the jobless module
// This uses the go module system to fetch the latest version of the jobless module
func (j *joblessGoRun) GoRunBlissRemote(ctx context.Context, stdOutReader func(stdOutPipe io.Reader) error, args ...string) error {
	_, logger, done := instrumentation.GetLogSpan(ctx, "GoRunBlissRemote")
	defer done()

	blissVersion := j.BlissVersion
	os.Setenv("GOPRIVATE", "github.com/weka")
	module := fmt.Sprintf("github.com/weka/bliss@%s", blissVersion)

	// Run go run jobless/main.go
	os.Setenv("AWS_REGION", "eu-west-1")

	runArgs := append([]string{"run", module}, args...)
	logger.V(1).Info("Command: go", "args", runArgs)
	cmd := exec.Command("go", runArgs...)

	stdOutPipe, err := cmd.StdoutPipe()
	if err != nil {
		logger.V(1).Error(err, "failed to create stdout pipe")
		return &BlissRunError{
			Err:     err,
			Message: "failed to create stdout pipe",
		}
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		logger.V(1).Error(err, "failed to create stderr pipe")
		return &BlissRunError{
			Err:     err,
			Message: "failed to create stderr pipe",
		}
	}

	if err := cmd.Start(); err != nil {
		logger.V(1).Error(err, "failed to start bliss")
		return &BlissRunError{
			Err:     err,
			Message: "failed to start bliss",
		}
	}

	// Stream STDERR
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			// logger.Info(scanner.Text())
			fmt.Println(scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			logger.V(1).Error(err, "failed to read stderr")
		}
	}()

	if err := stdOutReader(stdOutPipe); err != nil {
		return err
	}

	// Wait for command to finish
	if err = cmd.Wait(); err != nil {
		var message string
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message = fmt.Sprintf("failed to run bliss: %s", string(exitErr.Stderr))
		} else {
			message = "failed to run bliss"
		}
		logger.V(1).Info("Bliss non-zero exit code", "message", message, "err", err)
		return &BlissRunError{
			Err:     err,
			Message: message,
		}
	}

	return nil
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
