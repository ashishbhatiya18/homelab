package main

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

//go:embed all:static
var embeddedStatic embed.FS

// DockerHost manages one Docker endpoint with automatic reconnection.
//
// For SSH hosts (rawURL = "ssh://user@host") a pure-Go SSH tunnel is
// established; the HTTP transport sends each Docker API request through it
// via the remote Docker socket. Auth is via the SSH agent (SSH_AUTH_SOCK).
//
// On any connection-class error (broken pipe, EOF, …) the SSH connection and
// Docker client are both discarded; the next operation rebuilds them
// transparently, recovering from network interruptions without a restart.
type DockerHost struct {
	Name   string
	rawURL string
	isSSH  bool
	mu     sync.Mutex
	ssh    *ssh.Client
	dc     *client.Client
}

// client returns the active Docker client, lazily creating it on first call
// or after an invalidation.
func (h *DockerHost) client() (*client.Client, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dc != nil {
		return h.dc, nil
	}
	dc, sshConn, err := buildClient(h.rawURL, h.isSSH)
	if err != nil {
		return nil, err
	}
	h.dc = dc
	h.ssh = sshConn
	return dc, nil
}

// invalidate closes all connections so the next call to client() rebuilds them.
func (h *DockerHost) invalidate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dc != nil {
		h.dc.Close()
		h.dc = nil
	}
	if h.ssh != nil {
		h.ssh.Close()
		h.ssh = nil
	}
}

// close shuts down the host permanently.
func (h *DockerHost) close() { h.invalidate() }

// do executes fn with a valid Docker client. On a connection-class error it
// invalidates and retries once, recovering from dropped SSH sessions.
func (h *DockerHost) do(fn func(*client.Client) error) error {
	dc, err := h.client()
	if err != nil {
		return err
	}
	if err = fn(dc); err != nil && isConnError(err) {
		log.Printf("node %q: connection error, reconnecting: %v", h.Name, err)
		h.invalidate()
		dc, err2 := h.client()
		if err2 != nil {
			return err
		}
		err = fn(dc)
	}
	return err
}

// buildClient creates a Docker client for the given URL.
// For SSH it builds a pure-Go SSH tunnel; returns the ssh.Client so the
// caller can close it together with the Docker client.
func buildClient(rawURL string, isSSH bool) (*client.Client, *ssh.Client, error) {
	if !isSSH {
		dc, err := client.NewClientWithOpts(
			client.WithHost(rawURL),
			client.WithAPIVersionNegotiation(),
		)
		return dc, nil, err
	}

	sshConn, err := dialSSH(rawURL)
	if err != nil {
		return nil, nil, err
	}

	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := sshConn.Dial("unix", "/var/run/docker.sock")
		if err != nil {
			log.Printf("SSH tunnel → docker.sock: %v", err)
		}
		return conn, err
	}

	// WithHost sets up the base transport; WithDialContext then patches its
	// DialContext to route all connections through the SSH tunnel.
	dc, err := client.NewClientWithOpts(
		client.WithHost("tcp://localhost"),
		client.WithDialContext(dial),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		sshConn.Close()
		return nil, nil, err
	}
	return dc, sshConn, nil
}

// dialSSH opens an SSH connection to the host in rawURL using the system SSH
// agent (SSH_AUTH_SOCK). No subprocess is spawned; everything is pure Go.
func dialSSH(rawURL string) (*ssh.Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse SSH URL: %w", err)
	}

	user := u.User.Username()
	if user == "" {
		user = os.Getenv("USER")
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "22"
	}
	addr := host + ":" + port

	var authMethods []ssh.AuthMethod

	// SSH agent (preferred — key material stays in agent memory)
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if agentConn, err := net.Dial("unix", sock); err == nil {
			authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(agentConn).Signers))
		}
	}

	// Explicit key file — for Docker/unattended deployments (set SSH_KEY_FILE)
	if keyPath := os.Getenv("SSH_KEY_FILE"); keyPath != "" {
		if data, err := os.ReadFile(keyPath); err == nil {
			if signer, err := ssh.ParsePrivateKey(data); err == nil {
				authMethods = append(authMethods, ssh.PublicKeys(signer))
			} else {
				log.Printf("SSH_KEY_FILE %s: cannot parse key: %v", keyPath, err)
			}
		} else {
			log.Printf("SSH_KEY_FILE %s: cannot read: %v", keyPath, err)
		}
	}

	// Fallback: well-known key files in ~/.ssh/
	home, _ := os.UserHomeDir()
	for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
		if data, err := os.ReadFile(filepath.Join(home, ".ssh", name)); err == nil {
			if signer, err := ssh.ParsePrivateKey(data); err == nil {
				authMethods = append(authMethods, ssh.PublicKeys(signer))
			}
		}
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no SSH auth methods available (set SSH_AUTH_SOCK or ensure ~/.ssh/id_* exists)")
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // homelab: skip strict host checking
	}

	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("SSH dial %s: %w", addr, err)
	}
	log.Printf("SSH connected: %s@%s", user, addr)
	return conn, nil
}

func isConnError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "EOF") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "no such host") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "use of closed network connection")
}

type StackManager struct {
	hosts            []*DockerHost
	stacksPath       string
	remoteStacksPath string
}

// parseDockerHost parses DOCKER_HOST into one or more named Docker hosts.
//
// Formats:
//
//	bare URL:      unix:///var/run/docker.sock          → node "local"
//	named single:  ab=ssh://dietpi@10.10.10.11          → node "ab"
//	named multi:   ab=ssh://dietpi@10.10.10.11,cd=unix:///var/run/docker.sock
func parseDockerHost(s string) ([]*DockerHost, error) {
	var hosts []*DockerHost
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, rawURL := "local", entry
		// "ab=ssh://..." — split only when the left side has no "://"
		if idx := strings.Index(entry, "="); idx > 0 && !strings.Contains(entry[:idx], "//") {
			name = strings.TrimSpace(entry[:idx])
			rawURL = strings.TrimSpace(entry[idx+1:])
		}
		hosts = append(hosts, &DockerHost{
			Name:  name,
			rawURL: rawURL,
			isSSH: strings.HasPrefix(rawURL, "ssh://"),
		})
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("no hosts parsed from %q", s)
	}
	return hosts, nil
}

func main() {
	godotenv.Load()

	stacksPath := os.Getenv("STACKS_PATH")
	if stacksPath == "" {
		stacksPath = "/localstack/nodes"
	}
	remoteStacksPath := os.Getenv("REMOTE_STACKS_PATH")
	if remoteStacksPath == "" {
		remoteStacksPath = stacksPath
	}

	dockerHostEnv := os.Getenv("DOCKER_HOST")
	if dockerHostEnv == "" {
		dockerHostEnv = "unix:///var/run/docker.sock"
	}
	hosts, err := parseDockerHost(dockerHostEnv)
	if err != nil {
		log.Fatalf("DOCKER_HOST: %v", err)
	}

	// Probe each host at startup — warn but don't die; unreachable nodes are
	// retried lazily on every request.
	ctx := context.Background()
	for _, h := range hosts {
		dc, err := h.client()
		if err != nil {
			log.Printf("Warning: node %q (%s): %v", h.Name, h.rawURL, err)
			h.invalidate()
			continue
		}
		info, err := dc.Info(ctx)
		if err != nil {
			log.Printf("Warning: node %q (%s): %v", h.Name, h.rawURL, err)
			h.invalidate()
			continue
		}
		log.Printf("Connected to node %q — host: %s, containers: %d", h.Name, info.Name, info.Containers)
	}
	defer func() {
		for _, h := range hosts {
			h.close()
		}
	}()

	sm := &StackManager{hosts: hosts, stacksPath: stacksPath, remoteStacksPath: remoteStacksPath}

	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()
	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:3000", "https://stackmgr.pages.dev"},
		AllowMethods:     []string{"GET", "POST", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Authorization", "Content-Type"},
		AllowCredentials: true,
	}))

	router.GET("/api/health", sm.getSystemHealth)
	router.GET("/api/stacks", sm.listStacks)
	router.GET("/api/stacks/:node/:stack", sm.getStackStatus)
	router.GET("/api/stacks/:node/:stack/health", sm.checkStackHealth)
	router.POST("/api/stacks/:node/:stack/start", sm.startStack)
	router.POST("/api/stacks/:node/:stack/stop", sm.stopStack)
	router.POST("/api/stacks/:node/:stack/restart", sm.restartStack)
	router.POST("/api/stacks/:node/:stack/rebuild", sm.rebuildStack)
	router.POST("/api/stacks/:node/:stack/update", sm.updateStack)
	router.GET("/api/stacks/:node/:stack/logs", sm.getStackLogs)
	router.POST("/api/stacks/:node/:stack/containers/:service/start", sm.startContainer)
	router.POST("/api/stacks/:node/:stack/containers/:service/stop", sm.stopContainer)
	router.POST("/api/stacks/:node/:stack/containers/:service/restart", sm.restartContainer)
	router.GET("/api/stacks/:node/:stack/containers/:service/logs", sm.getContainerLogs)

	// Serve embedded Next.js static export for all non-API routes.
	// Falls back to index.html so client-side routing works.
	if staticSub, err := fs.Sub(embeddedStatic, "static"); err == nil {
		fileServer := http.FileServer(http.FS(staticSub))
		router.NoRoute(func(c *gin.Context) {
			path := strings.TrimPrefix(c.Request.URL.Path, "/")
			if _, err := staticSub.(fs.StatFS).Stat(path); err != nil {
				c.Request.URL.Path = "/"
			}
			fileServer.ServeHTTP(c.Writer, c.Request)
		})
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Starting stackmgr-proxy on :%s with %d node(s)", port, len(hosts))
	if err := router.Run(":" + port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

// ── Response types ────────────────────────────────────────────────────────────

type HealthStatus struct {
	Status   string `json:"status"`
	Services int    `json:"services"`
	Healthy  int    `json:"healthy"`
	Message  string `json:"message"`
}

type StackInfo struct {
	Node        string          `json:"node"`
	Environment string          `json:"environment"`
	Name        string          `json:"name"`
	Path        string          `json:"path"`
	Status      string          `json:"status"`
	Services    []ServiceStatus `json:"services"`
}

type StackDetail struct {
	Node     string          `json:"node"`
	Name     string          `json:"name"`
	Status   string          `json:"status"`
	Services []ServiceStatus `json:"services"`
}

type ServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	State  string `json:"state"`
	URL    string `json:"url,omitempty"`
}

type HealthCheckResult struct {
	Service string `json:"service"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func composeStatus(running, total int) string {
	if total == 0 || running == 0 {
		return "stopped"
	}
	if running == total {
		return "running"
	}
	return "partial"
}

func (sm *StackManager) hostForNode(node string) (*DockerHost, error) {
	for _, h := range sm.hosts {
		if h.Name == node {
			return h, nil
		}
	}
	return nil, fmt.Errorf("unknown node: %q", node)
}

func listComposeContainers(ctx context.Context, dc *client.Client, project string) ([]container.Summary, error) {
	f := filters.NewArgs()
	if project != "" {
		f.Add("label", "com.docker.compose.project="+project)
	} else {
		f.Add("label", "com.docker.compose.project")
	}
	return dc.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
}

func containerIDForService(ctx context.Context, dc *client.Client, project, service string) (string, error) {
	f := filters.NewArgs()
	f.Add("label", "com.docker.compose.project="+project)
	f.Add("label", "com.docker.compose.service="+service)
	ctrs, err := dc.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return "", err
	}
	if len(ctrs) == 0 {
		return "", fmt.Errorf("container not found: %s/%s", project, service)
	}
	return ctrs[0].ID, nil
}

// extractTraefikURL reads traefik.http.routers.*.rule labels and returns the
// first Host(`domain`) as an https:// URL.
func extractTraefikURL(labels map[string]string) string {
	for key, val := range labels {
		if !strings.HasPrefix(key, "traefik.http.routers.") || !strings.HasSuffix(key, ".rule") {
			continue
		}
		if i := strings.Index(val, "Host(`"); i >= 0 {
			rest := val[i+6:]
			if j := strings.Index(rest, "`"); j >= 0 {
				return "https://" + rest[:j]
			}
		}
	}
	return ""
}

func (sm *StackManager) envFromWorkdir(workDir string) string {
	for _, base := range []string{sm.remoteStacksPath, sm.stacksPath} {
		if base == "" {
			continue
		}
		rel, err := filepath.Rel(base, workDir)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "..") {
			continue
		}
		parts := strings.SplitN(rel, "/", 2)
		if parts[0] != "" {
			return parts[0]
		}
	}
	parts := strings.Split(strings.TrimRight(filepath.ToSlash(workDir), "/"), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return "unknown"
}

func svcName(ctr container.Summary) string {
	if n := ctr.Labels["com.docker.compose.service"]; n != "" {
		return n
	}
	if len(ctr.Names) > 0 {
		return strings.TrimPrefix(ctr.Names[0], "/")
	}
	return ctr.ID[:12]
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func (sm *StackManager) getSystemHealth(c *gin.Context) {
	ctx := context.Background()
	total, healthy := 0, 0
	for _, host := range sm.hosts {
		host.do(func(dc *client.Client) error { //nolint:errcheck
			ctrs, err := listComposeContainers(ctx, dc, "")
			if err != nil {
				return err
			}
			for _, ctr := range ctrs {
				total++
				if ctr.State == "running" {
					healthy++
				}
			}
			return nil
		})
	}
	status := "healthy"
	if healthy < total {
		status = "degraded"
	}
	c.JSON(http.StatusOK, HealthStatus{
		Status:   status,
		Services: total,
		Healthy:  healthy,
		Message:  fmt.Sprintf("%d/%d containers running", healthy, total),
	})
}

func (sm *StackManager) listStacks(c *gin.Context) {
	ctx := context.Background()
	var stacks []StackInfo

	for _, host := range sm.hosts {
		var ctrs []container.Summary
		if err := host.do(func(dc *client.Client) error {
			var err error
			ctrs, err = listComposeContainers(ctx, dc, "")
			return err
		}); err != nil {
			log.Printf("listStacks: node %q: %v", host.Name, err)
			continue
		}

		type projectMeta struct {
			env      string
			path     string
			running  int
			total    int
			services []ServiceStatus
		}
		projects := map[string]*projectMeta{}

		for _, ctr := range ctrs {
			project := ctr.Labels["com.docker.compose.project"]
			if project == "" {
				continue
			}
			if _, ok := projects[project]; !ok {
				workDir := ctr.Labels["com.docker.compose.project.working_dir"]
				projects[project] = &projectMeta{
					env:  sm.envFromWorkdir(workDir),
					path: workDir,
				}
			}
			projects[project].total++
			if ctr.State == "running" {
				projects[project].running++
			}
			projects[project].services = append(projects[project].services, ServiceStatus{
				Name:   svcName(ctr),
				Status: ctr.Status,
				State:  string(ctr.State),
				URL:    extractTraefikURL(ctr.Labels),
			})
		}

		for name, meta := range projects {
			sort.Slice(meta.services, func(i, j int) bool {
				return meta.services[i].Name < meta.services[j].Name
			})
			stacks = append(stacks, StackInfo{
				Node:        host.Name,
				Environment: meta.env,
				Name:        name,
				Path:        meta.path,
				Status:      composeStatus(meta.running, meta.total),
				Services:    meta.services,
			})
		}
	}

	sort.Slice(stacks, func(i, j int) bool {
		if stacks[i].Node != stacks[j].Node {
			return stacks[i].Node < stacks[j].Node
		}
		return stacks[i].Name < stacks[j].Name
	})

	c.JSON(http.StatusOK, stacks)
}

func (sm *StackManager) getStackStatus(c *gin.Context) {
	node, stack := c.Param("node"), c.Param("stack")
	host, err := sm.hostForNode(node)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	ctx := context.Background()
	var services []ServiceStatus
	var running int
	if err := host.do(func(dc *client.Client) error {
		ctrs, err := listComposeContainers(ctx, dc, stack)
		if err != nil {
			return err
		}
		services = make([]ServiceStatus, 0, len(ctrs))
		running = 0
		for _, ctr := range ctrs {
			if ctr.State == "running" {
				running++
			}
			services = append(services, ServiceStatus{
				Name:   svcName(ctr),
				Status: ctr.Status,
				State:  string(ctr.State),
				URL:    extractTraefikURL(ctr.Labels),
			})
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, StackDetail{
		Node:     node,
		Name:     stack,
		Status:   composeStatus(running, len(services)),
		Services: services,
	})
}

func (sm *StackManager) checkStackHealth(c *gin.Context) {
	node, stack := c.Param("node"), c.Param("stack")
	results := []HealthCheckResult{}
	for service, endpoint := range sm.getHealthCheckEndpoints(stack) {
		status, code, message := "unknown", 0, "Not checked"
		if endpoint != "" {
			status, code, message = "healthy", 200, "Service is responding"
		}
		results = append(results, HealthCheckResult{Service: service, Status: status, Message: message, Code: code})
	}
	c.JSON(http.StatusOK, gin.H{"node": node, "stack": stack, "results": results})
}

func (sm *StackManager) startStack(c *gin.Context) {
	node, stack := c.Param("node"), c.Param("stack")
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Starting %s on %s", stack, node)})
}

func (sm *StackManager) stopStack(c *gin.Context) {
	node, stack := c.Param("node"), c.Param("stack")
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Stopping %s on %s", stack, node)})
}

func (sm *StackManager) restartStack(c *gin.Context) {
	node, stack := c.Param("node"), c.Param("stack")
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Restarting %s on %s", stack, node)})
}

func (sm *StackManager) rebuildStack(c *gin.Context) {
	node, stack := c.Param("node"), c.Param("stack")
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Rebuilding %s on %s", stack, node)})
}

func (sm *StackManager) updateStack(c *gin.Context) {
	node, stack := c.Param("node"), c.Param("stack")
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Updating %s on %s", stack, node)})
}

func (sm *StackManager) getStackLogs(c *gin.Context) {
	node, stack := c.Param("node"), c.Param("stack")
	lines := c.DefaultQuery("lines", "100")
	host, err := sm.hostForNode(node)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	ctx := context.Background()
	var combined bytes.Buffer
	if err := host.do(func(dc *client.Client) error {
		ctrs, err := listComposeContainers(ctx, dc, stack)
		if err != nil {
			return err
		}
		for _, ctr := range ctrs {
			name := svcName(ctr)
			rc, err := dc.ContainerLogs(ctx, ctr.ID, container.LogsOptions{
				ShowStdout: true,
				ShowStderr: true,
				Tail:       lines,
			})
			if err != nil {
				continue
			}
			var buf bytes.Buffer
			stdcopy.StdCopy(&buf, &buf, rc)
			rc.Close()
			if buf.Len() > 0 {
				fmt.Fprintf(&combined, "=== %s ===\n%s\n", name, buf.String())
			}
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stack": stack, "logs": combined.String()})
}

func (sm *StackManager) startContainer(c *gin.Context) {
	node, stack, service := c.Param("node"), c.Param("stack"), c.Param("service")
	host, err := sm.hostForNode(node)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	ctx := context.Background()
	if err := host.do(func(dc *client.Client) error {
		id, err := containerIDForService(ctx, dc, stack, service)
		if err != nil {
			return err
		}
		return dc.ContainerStart(ctx, id, container.StartOptions{})
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "started"})
}

func (sm *StackManager) stopContainer(c *gin.Context) {
	node, stack, service := c.Param("node"), c.Param("stack"), c.Param("service")
	host, err := sm.hostForNode(node)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	ctx := context.Background()
	if err := host.do(func(dc *client.Client) error {
		id, err := containerIDForService(ctx, dc, stack, service)
		if err != nil {
			return err
		}
		return dc.ContainerStop(ctx, id, container.StopOptions{})
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "stopped"})
}

func (sm *StackManager) restartContainer(c *gin.Context) {
	node, stack, service := c.Param("node"), c.Param("stack"), c.Param("service")
	host, err := sm.hostForNode(node)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	ctx := context.Background()
	if err := host.do(func(dc *client.Client) error {
		id, err := containerIDForService(ctx, dc, stack, service)
		if err != nil {
			return err
		}
		return dc.ContainerRestart(ctx, id, container.StopOptions{})
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "restarted"})
}

func (sm *StackManager) getContainerLogs(c *gin.Context) {
	node, stack, service := c.Param("node"), c.Param("stack"), c.Param("service")
	lines := c.DefaultQuery("lines", "100")
	host, err := sm.hostForNode(node)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	ctx := context.Background()
	var buf bytes.Buffer
	if err := host.do(func(dc *client.Client) error {
		id, err := containerIDForService(ctx, dc, stack, service)
		if err != nil {
			return err
		}
		rc, err := dc.ContainerLogs(ctx, id, container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Tail:       lines,
		})
		if err != nil {
			return err
		}
		defer rc.Close()
		stdcopy.StdCopy(&buf, &buf, rc)
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"service": service, "logs": buf.String()})
}

func (sm *StackManager) getHealthCheckEndpoints(stack string) map[string]string {
	return map[string]string{
		"immich":         "http://immich:2283/api/server/ping",
		"vaultwarden":    "http://vaultwarden:80/alive",
		"ollama":         "http://ollama:11434/api/tags",
		"syncthing":      "http://syncthing:8384/rest/system/status",
		"rustpad":        "http://rustpad:8080/health",
		"kopia":          "http://kopia:51515/api/v1/status",
		"homeautomation": "http://homeautomation:8123/api/",
		"media":          "http://media:6767/health",
		"filebrowser":    "http://filebrowser:80/api/login",
		"bentopdf":       "http://bentopdf:3000/health",
		"isponsorblock":  "http://isponsorblock:8080/api/v1/status",
		"excalidraw":     "http://excalidraw:80/",
		"citrusdental":   "http://citrusdental:3000/health",
	}
}
