package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListPaginates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("account.id") != "acc" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		page := r.URL.Query().Get("page")
		fmt.Fprintf(w, `{"success":true,"result":[{"id":"z%s","name":"zone%s.in"}],"result_info":{"page":%s,"total_pages":2}}`, page, page, page)
	}))
	defer srv.Close()
	c := &Client{Token: "tok", Base: srv.URL}
	zones, err := c.Zones(context.Background(), "acc")
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 2 || zones[0].ID != "z1" || zones[1].Name != "zone2.in" {
		t.Fatalf("zones = %+v", zones)
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}],"result":null}`)
	}))
	defer srv.Close()
	_, err := (&Client{Token: "x", Base: srv.URL}).Tunnels(context.Background(), "acc")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 403 || !strings.Contains(err.Error(), "Authentication error (10000)") {
		t.Fatalf("err = %v", err)
	}
}

func TestTunnelConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/acc/cfd_tunnel/t1/configurations" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"success":true,"result":{"version":7,"config":{"ingress":[
			{"hostname":"a.ab18.in","service":"https://traefik:443","originRequest":{"httpHostHeader":"a.ab18.in"}},
			{"service":"http_status:404"}],"originRequest":{"http2Origin":true}}}}`)
	}))
	defer srv.Close()
	cfg, err := (&Client{Token: "x", Base: srv.URL}).TunnelConfig(context.Background(), "acc", "t1")
	if err != nil {
		t.Fatal(err)
	}
	in := cfg.Config.Ingress
	if cfg.Version != 7 || len(in) != 2 || in[0].OriginRequest["httpHostHeader"] != "a.ab18.in" || in[1].Hostname != "" {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.Config.OriginRequest["http2Origin"] != true {
		t.Fatalf("tunnel-wide originRequest = %v", cfg.Config.OriginRequest)
	}
}

func TestReadOnly(t *testing.T) {
	if !(DNSRecord{Meta: map[string]any{"read_only": true}}).ReadOnly() || (DNSRecord{}).ReadOnly() {
		t.Fatal("ReadOnly")
	}
}

func TestVisibleAccountsFromZones(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/accounts": // no Account Settings permission: empty, not an error
			fmt.Fprint(w, `{"success":true,"result":[],"result_info":{"page":1,"total_pages":0}}`)
		case "/zones":
			if r.URL.Query().Has("account.id") {
				t.Errorf("unexpected account filter: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"success":true,"result":[
				{"id":"z1","name":"a.in","account":{"id":"acc","name":"Mine"}},
				{"id":"z2","name":"b.in","account":{"id":"acc","name":"Mine"}}],"result_info":{"page":1,"total_pages":1}}`)
		}
	}))
	defer srv.Close()
	accs, err := (&Client{Token: "x", Base: srv.URL}).VisibleAccounts(context.Background())
	if err != nil || len(accs) != 1 || accs[0].ID != "acc" || accs[0].Name != "Mine" {
		t.Fatalf("accounts = %+v, %v", accs, err)
	}
}
