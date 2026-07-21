// Package oci
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
)

type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	// Clone standard transport to inherit OS-level TCP dialing without nil panics,
	// then apply defensive idle timeouts while keeping total stream duration unbounded (Timeout: 0).
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.IdleConnTimeout = 90 * time.Second

	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   0, // 0 removes execution ceilings so large blobs can stream over slow networks
		},
	}
}

func (c *Client) FetchManifest(imageRef string) (*Manifest, error) {
	registry, repo, reference := ParseImageRef(imageRef)
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, reference)

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

func (c *Client) DownloadBlob(ctx context.Context, repo string, descriptor Descriptor, destinationDir string) error {
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

		// Standard assignment (=) prevents variable shadowing of outer resp/err
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

func (c *Client) UploadBlob(ctx context.Context, repo string, digest string, filePath string) error {
	headURL := fmt.Sprintf("https://registry-1.docker.io/v2/%s/blobs/%s", repo, digest)
	headReq, err := http.NewRequestWithContext(ctx, "PUT", headURL, nil)
	if err != nil {
		return err
	}

	headResp, err := c.httpClient.Do(headReq)
	if err != nil {
		return err
	}
	headResp.Body.Close()

	if headResp.StatusCode == http.StatusUnauthorized {
		authHeader := headResp.Header.Get("Www-Authenticate")
		token, err := c.FetchBearerToken(authHeader)
		if err != nil {
			return fmt.Errorf("push auth failed: %v", err)
		}
		headReq.Header.Set("Authorization", "Bearer "+token)
		headResp, err = c.httpClient.Do(headReq)
		if err != nil {
			return err
		}
		headResp.Body.Close()
	}

	if headResp.StatusCode == http.StatusOK {
		fmt.Printf("[Skip] Blob %s already exists on destination.\n", digest[:15]+"...")
		return nil
	}

	postURL := fmt.Sprintf("https://registry-1.docker.io/v2/%s/blobs/uploads/", repo)
	postRequest, err := http.NewRequestWithContext(ctx, "POST", postURL, nil)
	if err != nil {
		return err
	}

	if auth := headReq.Header.Get("Authorization"); auth != "" {
		postRequest.Header.Set("Authorization", auth)
	}

	postResponse, err := c.httpClient.Do(postRequest)
	if err != nil {
		return err
	}
	defer postResponse.Body.Close()

	if postResponse.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(postRequest.Body)
		return fmt.Errorf("failed to initiate upload, status %d: %s", postResponse.StatusCode, body)
	}

	locationURL := postResponse.Header.Get("Location")
	if locationURL == "" {
		return fmt.Errorf("registry returned 202 Accepted without a Location Header")
	}

	var putURL string
	if strings.Contains(locationURL, "?") {
		putURL = fmt.Sprintf("%s&digest=%s", locationURL, digest)
	} else {
		putURL = fmt.Sprintf("%s?digest=%s", locationURL, digest)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open layer file: %v", err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return err
	}

	putReq, err := http.NewRequestWithContext(ctx, "PUT", putURL, nil)
	if err != nil {
		return err
	}

	putReq.ContentLength = fileInfo.Size()
	putReq.Header.Set("Content-Type", "application/octet-stream")

	if auth := headReq.Header.Get("Authorization"); auth != "" {
		putReq.Header.Set("Authorization", auth)
	}

	putResponse, err := c.httpClient.Do(putReq)
	if err != nil {
		return fmt.Errorf("upload stream failed: %v", err)
	}
	defer putResponse.Body.Close()

	if putResponse.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(putResponse.Body)
		return fmt.Errorf("registry rejected blob upload, status %d: %s", putResponse.StatusCode, body)
	}

	return nil
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
