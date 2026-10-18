// Package aria drives aria2c over JSON-RPC for torrents and HTTP/FTP
// downloads. One aria2c process serves a whole `down` run; it listens on
// localhost with a random secret and exits with us (--stop-with-process).
//
// Torrents never seed (--seed-time=0). Magnet metadata and .torrent files
// fetched by URL are kept in the download dir (--bt-save-metadata) so an
// interrupted magnet resumes without refetching it; the caller removes them
// with the rest of the staging directory.
package aria

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Options struct {
	CacheDir    string // DHT routing table
	Trackers    []string
	Connections int
	MaxDown     string
	MaxUp       string
}

type Client struct {
	cmd    *exec.Cmd
	url    string
	secret string
	hc     *http.Client
	exited chan struct{}

	mu     sync.Mutex
	stderr bytes.Buffer
}

func (c *Client) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stderr.Write(p)
}

// Args are aria2c's options for a run (exported for tests).
func Args(o Options, port int, secret string) []string {
	n := o.Connections
	if n < 1 || n > 16 {
		n = 16
	}
	args := []string{
		"--enable-rpc", "--rpc-listen-all=false",
		"--rpc-listen-port=" + strconv.Itoa(port), "--rpc-secret=" + secret,
		"--rpc-save-upload-metadata=false",
		"--stop-with-process=" + strconv.Itoa(os.Getpid()),
		"--quiet=true",
		"--continue=true", "--auto-file-renaming=false", "--allow-overwrite=false",
		"--file-allocation=none", "--max-tries=10", "--retry-wait=3",
		"--max-connection-per-server=" + strconv.Itoa(n), "--split=" + strconv.Itoa(n), "--min-split-size=1M",
		"--content-disposition-default-utf8=true",
		"--follow-torrent=mem", "--pause-metadata=true",
		"--bt-save-metadata=true", "--bt-load-saved-metadata=true",
		"--bt-remove-unselected-file=true", "--seed-time=0",
		"--enable-dht=true", "--dht-file-path=" + filepath.Join(o.CacheDir, "dht.dat"),
	}
	if len(o.Trackers) > 0 {
		args = append(args, "--bt-tracker="+strings.Join(o.Trackers, ","))
	}
	if o.MaxDown != "" {
		args = append(args, "--max-overall-download-limit="+o.MaxDown)
	}
	if o.MaxUp != "" {
		args = append(args, "--max-overall-upload-limit="+o.MaxUp)
	}
	return args
}

// Start launches aria2c and waits until its RPC answers.
func Start(o Options) (*Client, error) {
	bin, err := exec.LookPath("aria2c")
	if err != nil {
		return nil, errors.New("aria2c not found (see down doctor)")
	}
	if err := os.MkdirAll(o.CacheDir, 0o755); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	b := make([]byte, 16)
	rand.Read(b)
	c := &Client{
		url:    fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", port),
		secret: hex.EncodeToString(b),
		hc:     &http.Client{Timeout: 10 * time.Second},
		exited: make(chan struct{}),
	}
	c.cmd = exec.Command(bin, Args(o, port, c.secret)...)
	// Own process group: Ctrl-C reaches us, and we shut aria2c down
	// cleanly (it saves its .aria2 control files for resuming).
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.cmd.Stderr = c
	if err := c.cmd.Start(); err != nil {
		return nil, err
	}
	go func() { c.cmd.Wait(); close(c.exited) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := c.call(ctx, "aria2.getVersion", nil, nil)
		cancel()
		if err == nil {
			return c, nil
		}
		select {
		case <-c.exited:
			return nil, fmt.Errorf("aria2c exited: %s", strings.TrimSpace(c.output()))
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			c.Close()
			return nil, fmt.Errorf("aria2c RPC did not come up: %v", err)
		}
	}
}

func (c *Client) output() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stderr.String()
}

// Close shuts aria2c down, saving control files of unfinished downloads.
func (c *Client) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c.call(ctx, "aria2.forceShutdown", nil, nil)
	select {
	case <-c.exited:
	case <-time.After(10 * time.Second):
		c.cmd.Process.Kill()
		<-c.exited
	}
}

func (c *Client) call(ctx context.Context, method string, params []any, out any) error {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "down", "method": method,
		"params": append([]any{"token:" + c.secret}, params...),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("aria2 %s: %w", method, err)
	}
	if r.Error != nil {
		return fmt.Errorf("aria2 %s: %s", method, r.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

type File struct {
	Index           int    `json:"index,string"`
	Path            string `json:"path"`
	Length          int64  `json:"length,string"`
	CompletedLength int64  `json:"completedLength,string"`
	Selected        bool   `json:"selected,string"`
}

type Status struct {
	GID             string   `json:"gid"`
	Status          string   `json:"status"`
	TotalLength     int64    `json:"totalLength,string"`
	CompletedLength int64    `json:"completedLength,string"`
	DownloadSpeed   int64    `json:"downloadSpeed,string"`
	Connections     int      `json:"connections,string"`
	NumSeeders      int      `json:"numSeeders,string"`
	FollowedBy      []string `json:"followedBy"`
	ErrorCode       string   `json:"errorCode"`
	ErrorMessage    string   `json:"errorMessage"`
	Files           []File   `json:"files"`
	Bittorrent      *struct {
		Info *struct {
			Name string `json:"name"`
		} `json:"info"`
	} `json:"bittorrent"`
}

func (s Status) torrentName() string {
	if s.Bittorrent != nil && s.Bittorrent.Info != nil {
		return s.Bittorrent.Info.Name
	}
	return ""
}

// Name is the torrent's name, else the first file's name.
func (s Status) Name() string {
	if n := s.torrentName(); n != "" {
		return n
	}
	if len(s.Files) > 0 && s.Files[0].Path != "" {
		return filepath.Base(s.Files[0].Path)
	}
	return ""
}

func (c *Client) status(ctx context.Context, gid string) (Status, error) {
	var s Status
	err := c.call(ctx, "aria2.tellStatus", []any{gid}, &s)
	return s, err
}

type Request struct {
	URI     string // magnet link or URL
	Torrent []byte // contents of a local .torrent file (instead of URI)
	Dir     string
	// Select returns the aria2 file indexes (1-based) to download from a
	// torrent once its file list is known; nil downloads everything.
	Select   func(name string, files []File) ([]int, error)
	Progress func(Progress)
}

type Progress struct {
	Name         string
	Torrent      bool
	Metadata     bool // still fetching a magnet's metadata / a .torrent
	Done, Total  int64
	Speed        int64
	Peers, Seeds int
}

type Result struct {
	Name  string
	Files []string // absolute paths of the downloaded (selected) files
	Total int64
}

// Download runs one request to completion. On ctx cancellation the download
// is left in aria2, so Close saves it for resuming.
func (c *Client) Download(ctx context.Context, r Request) (Result, error) {
	opts := map[string]string{"dir": r.Dir}
	var gid string
	var err error
	if r.Torrent != nil {
		opts["pause"] = "true" // pick files first
		err = c.call(ctx, "aria2.addTorrent", []any{base64.StdEncoding.EncodeToString(r.Torrent), []string{}, opts}, &gid)
	} else {
		err = c.call(ctx, "aria2.addUri", []any{[]string{r.URI}, opts}, &gid)
	}
	if err != nil {
		return Result{}, err
	}
	handled := map[string]bool{} // file selection done for this gid
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		st, err := c.status(ctx, gid)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return Result{}, err
		}
		if st.torrentName() != "" && !handled[gid] && st.Status != "complete" {
			handled[gid] = true
			if err := c.selectFiles(ctx, gid, st, r.Select); err != nil {
				return Result{}, err
			}
			continue
		}
		switch st.Status {
		case "complete":
			if len(st.FollowedBy) > 0 {
				gid = st.FollowedBy[len(st.FollowedBy)-1]
				continue
			}
			res := Result{Name: st.Name(), Total: st.TotalLength}
			for _, f := range st.Files {
				if f.Selected && !strings.HasPrefix(f.Path, "[METADATA]") {
					res.Files = append(res.Files, f.Path)
				}
			}
			return res, nil
		case "error":
			msg := st.ErrorMessage
			if msg == "" {
				msg = "aria2 error " + st.ErrorCode
			}
			return Result{}, errors.New(msg)
		case "removed":
			return Result{}, errors.New("download was removed")
		}
		if r.Progress != nil {
			r.Progress(Progress{
				Name: st.Name(), Torrent: st.Bittorrent != nil,
				Metadata: st.Bittorrent != nil && st.torrentName() == "",
				Done:     st.CompletedLength, Total: st.TotalLength, Speed: st.DownloadSpeed,
				Peers: st.Connections, Seeds: st.NumSeeders,
			})
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-c.exited:
			return Result{}, fmt.Errorf("aria2c exited: %s", strings.TrimSpace(c.output()))
		case <-tick.C:
		}
	}
}

// selectFiles applies sel to a torrent whose file list is known. A changed
// selection needs the download paused (aria2 only changes select-file then);
// downloads paused for metadata are resumed either way.
func (c *Client) selectFiles(ctx context.Context, gid string, st Status, sel func(string, []File) ([]int, error)) error {
	paused := st.Status == "paused"
	var idx []int
	if sel != nil {
		var err error
		if idx, err = sel(st.torrentName(), st.Files); err != nil {
			return err
		}
		if len(idx) == 0 {
			return errors.New("no files selected")
		}
	}
	if sel != nil && len(idx) < len(st.Files) {
		if !paused {
			if err := c.call(ctx, "aria2.forcePause", []any{gid}, nil); err != nil {
				return err
			}
			if err := c.waitStatus(ctx, gid, "paused"); err != nil {
				return err
			}
			paused = true
		}
		parts := make([]string, len(idx))
		for i, n := range idx {
			parts[i] = strconv.Itoa(n)
		}
		if err := c.call(ctx, "aria2.changeOption", []any{gid, map[string]string{"select-file": strings.Join(parts, ",")}}, nil); err != nil {
			return err
		}
	}
	if paused {
		return c.call(ctx, "aria2.unpause", []any{gid}, nil)
	}
	return nil
}

func (c *Client) waitStatus(ctx context.Context, gid, want string) error {
	for range 100 {
		st, err := c.status(ctx, gid)
		if err != nil {
			return err
		}
		if st.Status == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("download did not become %s", want)
}
