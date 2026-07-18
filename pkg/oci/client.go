// Package oci handles authentication and communication
package oci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/devxdh/shipwright/pkg/helpers"
)

type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.IdleConnTimeout = 90 * time.Second

	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   0, // 0 removes the 15-second execution limit so large layers can stream
		},
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

	if isIndexMediaType(manifest.MediaType) {
		targetDigest, err := resolvePlatformDigest(manifest.Manifests, "linux", "amd64")
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

func isIndexMediaType(mediaType string) bool {
	return mediaType == "application/vnd.oci.image.index.v1+json" ||
		mediaType == "application/vnd.docker.distribution.manifest.list.v2+json"
}

func resolvePlatformDigest(manifests []Descriptor, targetOS, targetArch string) (string, error) {
	for _, desc := range manifests {
		if desc.Platform != nil {
			if desc.Platform.OS == targetOS && desc.Platform.Architecture == targetArch {
				return desc.Digest, nil
			}
		}
	}

	return "", fmt.Errorf("no manifest found for platform %s/%s", targetOS, targetArch)
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
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		authHeader := resp.Header.Get("Www-Authenticate")
		if authHeader == "" {
			return fmt.Errorf("registry returned 401 with no Www-Authenticate header")
		}

		token, err := c.FetchBearerToken(authHeader)
		if err != nil {
			return fmt.Errorf("auth challenge failed: %v", err)
		}

		resp.Body.Close()

		req.Header.Set("Authorization", "Bearer "+token)

		resp, err = c.httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("registry returned status %d for blob %s: %s", resp.StatusCode, descriptor.Digest, body)
	}

	if err := os.MkdirAll(destinationDir, 0755); err != nil {
		return err
	}

	fileName := filepath.Join(destinationDir, strings.ReplaceAll(descriptor.Digest, ":", "-")+".tar")
	tempFile, err := os.CreateTemp(destinationDir, "download-*.tmp")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()

	defer func() {
		tempFile.Close()
		if err != nil {
			os.Remove(tempPath)
		}
	}()

	hasher := sha256.New()
	teeReader := io.TeeReader(resp.Body, hasher)

	_, err = io.Copy(tempFile, teeReader)
	if err != nil {
		return fmt.Errorf("stream copy failed: %v", err)
	}

	computedHex := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if computedHex != descriptor.Digest {
		err = fmt.Errorf("INTEGRITY VIOLATION: expected %s, computed %s", descriptor.Digest, computedHex)
		return err
	}

	tempFile.Close()

	err = os.Rename(tempPath, fileName)
	if err != nil {
		return fmt.Errorf("failed to finalize blob file: %v", err)
	}

	return nil
}
