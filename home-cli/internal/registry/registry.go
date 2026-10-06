// Package registry asks container registries for an image tag's current
// digest with a HEAD request — nothing is pulled and, on Docker Hub, HEAD
// requests do not count towards pull rate limits.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Ref is a parsed image reference.
type Ref struct {
	Registry, Repo, Tag string
}

// Parse splits "nginx", "ghcr.io/org/app:1.2" etc. into parts, applying
// Docker's defaults. Digest-pinned refs return an error (nothing to check).
func Parse(image string) (Ref, error) {
	if strings.Contains(image, "@") {
		return Ref{}, errors.New("pinned by digest")
	}
	name, tag := image, "latest"
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		name, tag = image[:i], image[i+1:]
	}
	reg := "registry-1.docker.io"
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 2 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		reg, name = parts[0], parts[1]
		if reg == "docker.io" {
			reg = "registry-1.docker.io"
		}
	}
	if reg == "registry-1.docker.io" && !strings.Contains(name, "/") {
		name = "library/" + name
	}
	return Ref{Registry: reg, Repo: name, Tag: tag}, nil
}

var accept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

var client = &http.Client{Timeout: 20 * time.Second}

// Cred is a registry login (for private images); nil means anonymous.
type Cred struct{ User, Password string }

// Digest returns the registry's current digest for ref's tag.
func Digest(ctx context.Context, r Ref, cred *Cred) (string, error) {
	u := fmt.Sprintf("https://%s/v2/%s/manifests/%s", r.Registry, r.Repo, r.Tag)
	resp, err := head(ctx, u, "")
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		tok, err := token(ctx, resp.Header.Get("WWW-Authenticate"), r.Repo, cred)
		if err != nil {
			return "", err
		}
		if resp, err = head(ctx, u, tok); err != nil {
			return "", err
		}
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry answered %s", resp.Status)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", errors.New("registry returned no digest")
	}
	return d, nil
}

func head(ctx context.Context, u, tok string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	return resp, nil
}

// token gets an anonymous pull token from the realm named in a Bearer challenge.
func token(ctx context.Context, challenge, repo string, cred *Cred) (string, error) {
	if !strings.HasPrefix(challenge, "Bearer ") {
		return "", fmt.Errorf("unsupported auth challenge %q", challenge)
	}
	params := map[string]string{}
	for _, kv := range strings.Split(strings.TrimPrefix(challenge, "Bearer "), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if ok {
			params[k] = strings.Trim(v, `"`)
		}
	}
	realm := params["realm"]
	if realm == "" {
		return "", errors.New("auth challenge without realm")
	}
	q := url.Values{}
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	q.Set("scope", scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if cred != nil {
		req.SetBasicAuth(cred.User, cred.Password)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if cred == nil {
			return "", fmt.Errorf("private image — run `home registry login %s`", hostOf(realm))
		}
		return "", fmt.Errorf("token request: %s (check the saved login)", resp.Status)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	if t.Token != "" {
		return t.Token, nil
	}
	return t.AccessToken, nil
}

func hostOf(realm string) string {
	u, err := url.Parse(realm)
	if err != nil {
		return realm
	}
	return u.Host
}
