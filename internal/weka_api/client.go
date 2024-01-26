package weka_api

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/pkg/errors"
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

func NewWekaRestApiClient(host string) (*WekaRestApiClient, error) {
	baseUrl := fmt.Sprintf("https://%s:14000/api/v2", host)
	clientImpl, err := NewClientWithResponses(baseUrl, disableHostKeyChecking)
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
	logger := log.FromContext(ctx)
	logger.Info("Login request", "request", request)

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
