package weka_api

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strconv"

	"github.com/deepmap/oapi-codegen/v2/pkg/securityprovider"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

//go:generate go run github.com/deepmap/oapi-codegen/v2/cmd/oapi-codegen@v2.0.0 --config client.cfg.yaml weka_api_4.2.json

type WekaRestApiClient struct {
	ClientImpl *ClientWithResponses
	ApiKey     *ApiKey
}

type ApiKey struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

func NewAnonymousClient(host string) (*WekaRestApiClient, error) {
	return NewWekaRestApiClient(host, nil)
}

func NewWekaRestApiClient(host string, accessToken *string) (*WekaRestApiClient, error) {
	baseUrl := fmt.Sprintf("https://%s:14000/api/v2", host)

	clientOptions := []ClientOption{
		disableHostKeyChecking,
	}

	if accessToken != nil {
		tokenProvider, err := securityprovider.NewSecurityProviderBearerToken(*accessToken)
		if err != nil {
			return nil, errors.Wrap(err, "creating security token provider")
		}
		clientOptions = append(clientOptions, WithRequestEditorFn(tokenProvider.Intercept))
	}

	clientImpl, err := NewClientWithResponses(baseUrl, clientOptions...)
	if err != nil {
		return nil, errors.Wrap(err, "error creating client")
	}

	return &WekaRestApiClient{
		ClientImpl: clientImpl,
	}, nil
}

func disableHostKeyChecking(client *Client) error {
	if client.Client == nil {
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		}
		client.Client = &http.Client{
			Transport: transport,
		}

	}

	client.Client.(*http.Client).Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify = true
	return nil
}

func (c *WekaRestApiClient) Login(ctx context.Context, username, password string) error {
	if c.ClientImpl == nil {
		return errors.New("client not initialized")
	}

	org := "root"
	request := LoginJSONRequestBody{
		Username: username,
		Password: password,
		Org:      &org,
	}

	resp, err := c.ClientImpl.LoginWithResponse(ctx, request)
	if err != nil {
		return err
	}

	if resp.StatusCode() != http.StatusOK {
		return errors.Errorf("Login failed: %s", resp.Body)
	}

	c.ApiKey = &ApiKey{
		AccessToken:  *resp.JSON200.Data.AccessToken,
		RefreshToken: *resp.JSON200.Data.RefreshToken,
		TokenType:    *resp.JSON200.Data.TokenType,
	}

	return nil
}

// GetProcessList returns a list of processes running on the cluster
func (c *WekaRestApiClient) GetProcessList(ctx context.Context) ([]v1alpha1.Process, error) {
	if c.ClientImpl == nil {
		return nil, errors.New("client not initialized")
	}

	if c.ApiKey == nil {
		return nil, errors.New("API key not initialized, call Login first")
	}

	resp, err := c.ClientImpl.GetProcessesWithResponse(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "error getting process list")
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, errors.Errorf("GetProcesses failed: %s", resp.Body)
	}
	logger := log.FromContext(ctx)
	logger.Info("GetProcesses response", "body", resp.Body)

	processListExt := resp.JSON200.Data
	processList := []v1alpha1.Process{}
	for _, process := range *processListExt {
		containerPid, err := strconv.ParseInt(*process.ContainerId, 10, 32)
		if err != nil {
			containerPid = -1
		}

		processList = append(processList, v1alpha1.Process{
			APIPort:      int32(*process.MgmtPort),
			ContainerPid: int32(containerPid),
			InternalStatus: v1alpha1.InternalStatus{
				DisplayStatus: *process.Status,
				Message:       *process.Status,
				State:         *process.Status,
			},
			IsDisabled:      false,
			IsManaged:       false,
			IsMonitoring:    false,
			IsPersistent:    false,
			IsRunning:       false,
			LastFailure:     "",
			LastFailureText: "",
			LastFailureTime: "",
			Name:            *process.ContainerName,
			RunStatus:       *process.Status,
			Uptime:          "",
			VersionName:     *process.SwVersion,
		})

	}

	return processList, nil
}
