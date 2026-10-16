package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type got struct {
	path, auth string
	body       map[string]string
}

func server(t *testing.T, status int, seen *[]got) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		*seen = append(*seen, got{r.URL.Path, r.Header.Get("Authorization"), b})
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func settings(t *testing.T, m map[string]string) {
	old := get
	get = func(a string) string { return m[a] }
	t.Cleanup(func() { get = old })
}

func TestPushPrimary(t *testing.T) {
	var seen []got
	srv := server(t, 200, &seen)
	settings(t, map[string]string{AccountURL: srv.URL + "/homelab", AccountToken: "tk_x"})

	used, err := Push(context.Background(), "home: deployed ✓", "ab: network")
	if err != nil || used != srv.URL+"/homelab" {
		t.Fatalf("Push = %q, %v", used, err)
	}
	want := got{"/", "Bearer tk_x", map[string]string{"topic": "homelab", "title": "home: deployed ✓", "message": "ab: network"}}
	if len(seen) != 1 || seen[0].path != want.path || seen[0].auth != want.auth || seen[0].body["topic"] != "homelab" ||
		seen[0].body["title"] != want.body["title"] || seen[0].body["message"] != want.body["message"] {
		t.Fatalf("server saw %+v, want %+v", seen, want)
	}
}

func TestPushFallsBackWithoutToken(t *testing.T) {
	var down, up []got
	bad := server(t, 502, &down)
	good := server(t, 200, &up)
	settings(t, map[string]string{
		AccountURL: bad.URL + "/homelab", AccountToken: "tk_x",
		AccountFallback: good.URL + "/sub/ab18-secret",
	})

	used, err := Push(context.Background(), "t", "m")
	if err != nil || used != good.URL+"/sub/ab18-secret" {
		t.Fatalf("Push = %q, %v", used, err)
	}
	if len(down) != 1 || len(up) != 1 || up[0].path != "/sub" || up[0].auth != "" || up[0].body["topic"] != "ab18-secret" {
		t.Fatalf("primary saw %+v, fallback saw %+v", down, up)
	}
}

func TestPushErrors(t *testing.T) {
	settings(t, map[string]string{})
	if _, err := Push(context.Background(), "t", "m"); err != ErrNotConfigured {
		t.Fatalf("unconfigured: %v", err)
	}
	var seen []got
	bad := server(t, 403, &seen)
	settings(t, map[string]string{AccountURL: bad.URL + "/homelab"})
	if _, err := Push(context.Background(), "t", "m"); err == nil {
		t.Fatal("403 should fail")
	}
	settings(t, map[string]string{AccountURL: "https://ntfy.example/"})
	if _, err := Push(context.Background(), "t", "m"); err == nil {
		t.Fatal("URL without a topic should fail")
	}
}
