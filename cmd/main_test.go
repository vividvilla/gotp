package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vividvilla/gotp"
	"time"
)

func TestParseFlags(t *testing.T) {
	opts, data, err := parseFlags([]string{"--text", "--stub-funcs", "-b", "base.tmpl", "-d", `{"a":1}`, "x.tmpl"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.tmplPath != "x.tmpl" {
		t.Errorf("tmplPath = %q", opts.tmplPath)
	}
	if !opts.gotpCfg.Text || !opts.gotpCfg.StubFuncs {
		t.Errorf("cfg = %+v", opts.gotpCfg)
	}
	if len(opts.basePaths) != 1 || opts.basePaths[0] != "base.tmpl" {
		t.Errorf("basePaths = %q", opts.basePaths)
	}
	if m, ok := data.(map[string]any); !ok || m["a"] != 1.0 {
		t.Errorf("data = %#v", data)
	}
	// The server should not be reachable from the network by default.
	if !strings.HasPrefix(opts.addr, "127.0.0.1") {
		t.Errorf("addr = %q, want a loopback default", opts.addr)
	}
}

func TestParseFlagsErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		// A flag with no template used to panic on args[0].
		{"flag but no template", []string{"--sprig"}},
		{"data and data-file", []string{"-d", "{}", "--data-file", "d.json", "x.tmpl"}},
		{"bad json", []string{"-d", "{oops", "x.tmpl"}},
		{"missing data file", []string{"--data-file", "testdata/nope.json", "x.tmpl"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parseFlags(tc.args); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestParseFlagsTopLevelArray(t *testing.T) {
	_, data, err := parseFlags([]string{"-d", `[1,2]`, "x.tmpl"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := data.([]any); !ok {
		t.Errorf("data = %#v, want a list", data)
	}
}

func TestMatches(t *testing.T) {
	patterns := []string{"base/*.tmpl", "sample.tmpl"}
	for _, p := range []string{"sample.tmpl", "base/a.tmpl", "/abs/base/b.tmpl"} {
		if !matches(patterns, p) {
			t.Errorf("%q should match", p)
		}
	}
	for _, p := range []string{"other.txt", "sample.tmpl.swp", "base/a.html"} {
		if matches(patterns, p) {
			t.Errorf("%q should not match", p)
		}
	}
}

// TestStoreConcurrent is the regression test for the data race between the web
// UI updating the data and requests rendering with it. Run with -race.
func TestStoreConcurrent(t *testing.T) {
	s := &store{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			s.set(map[string]any{"n": i})
		}(i)
		go func() {
			defer wg.Done()
			_ = s.get()
		}()
	}
	wg.Wait()
}

// testServer builds a server over a temporary template.
func testServer(t *testing.T, src string, cfg gotp.Config) (*httptest.Server, *store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.tmpl")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := &options{tmplPath: path, gotpCfg: cfg}
	s := &store{}
	srv := httptest.NewServer(newMux(opts, s, newBroker()))
	t.Cleanup(srv.Close)
	return srv, s
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b := make([]byte, 0, 1024)
	buf := make([]byte, 512)
	for {
		n, err := res.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
	}
	return res, string(b)
}

func TestHandlers(t *testing.T) {
	srv, data := testServer(t, `<p>{{ .name }}</p>`, gotp.Config{})
	data.set(map[string]any{"name": "Gopher"})

	t.Run("index declares html", func(t *testing.T) {
		res, body := get(t, srv.URL+"/")
		if ct := res.Header.Get("Content-Type"); ct != "text/html" {
			t.Errorf("Content-Type = %q", ct)
		}
		if !strings.Contains(body, "<!DOCTYPE html>") {
			t.Error("index page not served")
		}
	})

	t.Run("out renders", func(t *testing.T) {
		res, body := get(t, srv.URL+"/out")
		if ct := res.Header.Get("Content-Type"); ct != "text/html" {
			t.Errorf("Content-Type = %q", ct)
		}
		if body != "<p>Gopher</p>" {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("source", func(t *testing.T) {
		_, body := get(t, srv.URL+"/source")
		if body != `<p>{{ .name }}</p>` {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("fields and skeleton", func(t *testing.T) {
		res, body := get(t, srv.URL+"/fields")
		if ct := res.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		var resp struct {
			Data struct {
				Fields   []string       `json:"fields"`
				Skeleton map[string]any `json:"skeleton"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		if len(resp.Data.Fields) != 1 || resp.Data.Fields[0] != ".name" {
			t.Errorf("fields = %q", resp.Data.Fields)
		}
		if _, ok := resp.Data.Skeleton["name"]; !ok {
			t.Errorf("skeleton = %#v", resp.Data.Skeleton)
		}
	})

	t.Run("data round trip", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/data", strings.NewReader(`{"name":"Rambo"}`))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", res.StatusCode)
		}
		if _, body := get(t, srv.URL+"/out"); body != "<p>Rambo</p>" {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("bad data is rejected", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/data", strings.NewReader(`{oops`))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d", res.StatusCode)
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		res, err := http.Post(srv.URL+"/out", "text/plain", nil)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("status = %d", res.StatusCode)
		}
	})
}

// A template error must reach the preview escaped, not break the page. The
// error text below quotes the failing action as "<len .a>", so an unescaped
// message would inject a tag.
func TestOutRendersErrorsEscaped(t *testing.T) {
	srv, _ := testServer(t, `{{ len .a }}`, gotp.Config{})
	_, body := get(t, srv.URL+"/out")
	if !strings.Contains(body, "<pre") {
		t.Fatalf("error not rendered as a block: %s", body)
	}
	if !strings.Contains(body, "&lt;len .a&gt;") || strings.Contains(body, "<len .a>") {
		t.Errorf("error message was not escaped: %s", body)
	}
}

func TestOutServesTextMode(t *testing.T) {
	srv, data := testServer(t, `{{ .v }}`, gotp.Config{Text: true})
	data.set(map[string]any{"v": "a & b"})
	res, body := get(t, srv.URL+"/out")
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	if body != "a & b" {
		t.Errorf("body = %q", body)
	}
}

// The watcher must survive the atomic rename that editors use to save.
func TestWatchSurvivesAtomicSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.tmpl")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}

	changed := make(chan struct{}, 10)
	fw, err := watch([]string{path}, func() { changed <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()

	save := func(body string) {
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
	}

	// Two saves in a row: the second is the one the old per-file watch
	// missed, because the first rename replaced the watched file.
	for i, body := range []string{"two", "three"} {
		save(body)
		select {
		case <-changed:
		case <-time.After(3 * time.Second):
			t.Fatalf("no change reported for save %d", i+1)
		}
	}
}
