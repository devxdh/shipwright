// Package oci handles authentication and communication
package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/devxdh/shipwright/pkg/helpers"
)

type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) FetchManifest(imageRef string) (*Manifest, error) {
	registry, repo, tag := helpers.ParseImageRef(imageRef)
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, tag)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set(
		"Accept",
		"application/vnd.oci.image.manifest.v1+json, "+
			"application/vnd.docker.distribution.manifest.v2+json, "+
			"application/vnd.oci.image.index.v1+json, "+
			"application/vnd.docker.distribution.manifest.list.v2+json",
	)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		authHeader := resp.Header.Get("Www-Authenticate")
		token, err := c.FetchBearerToken(authHeader)
		if err != nil {
			return nil, fmt.Errorf("auth challenge failed: %v", err)
		}

		req.Header.Set("Authorization", "Bearer "+token)
		resp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("registry returned status %d: %s", resp.StatusCode, body)
	}

	var manifest Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("failed to decode manifest JSON: %v", err)
	}

	if helpers.IsIndexMediaType(manifest.MediaType) {
		targetDigest, err := helpers.ResolvePlatformDigest(manifest.Manifests, "linux", "amd64")
		if err != nil {
			return nil, fmt.Errorf("platform resolution failed: %v", err)
		}

		resolvedRef := fmt.Sprintf("%s@%s", repo, targetDigest)
		return c.FetchManifest(resolvedRef)
	}

	return &manifest, nil
}

func (c *Client) FetchBearerToken(authHeader string) (string, error) {
	realmRe := regexp.MustCompile(`realm="([^"]+)"`)
	serviceRe := regexp.MustCompile(`service="([^"]+)"`)
	scopeRe := regexp.MustCompile(`scope="([^"]+)"`)

	realmMatch := realmRe.FindStringSubmatch(authHeader)
	serviceMatch := serviceRe.FindStringSubmatch(authHeader)
	scopeMatch := scopeRe.FindStringSubmatch(authHeader)

	if len(realmMatch) < 2 || len(serviceMatch) < 2 || len(scopeMatch) < 2 {
		return "", fmt.Errorf("invalid Www-Authenticate header format: %s", authHeader)
	}

	tokenURL := fmt.Sprintf("%s?service=%s&scope=%s", realmMatch[1], serviceMatch[1], scopeMatch[1])

	resp, err := c.httpClient.Get(tokenURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token server returned status %d", resp.StatusCode)
	}

	var authResp AuthToken
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return "", err
	}

	if authResp.Token != "" {
		return authResp.Token, nil
	}

	return authResp.AccessToken, nil
}

func (c *Client) DownloadBlob(
	ctx context.Context,
	repo string,
	descriptor Descriptor,
	destinationDir string,
) error {
	url := fmt.Sprintf("https://registry-1.docker.io/v2/%s/blobs/%s", repo, descriptor.Digest)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	return nil
}
