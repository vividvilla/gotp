package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vividvilla/gotp"
)

// testSession builds a server over the given input.
func testSession(t *testing.T, in *input, data any) (*httptest.Server, *session) {
	t.Helper()
	s := &session{in: in, broker: newBroker(), data: data, entry: in.entry}
	srv := httptest.NewServer(newServer(s))
	t.Cleanup(srv.Close)
	return srv, s
}

// fileSession builds a server over a single template written to a temp dir.
func fileSession(t *testing.T, body string, cfg gotp.Config, data any) (*httptest.Server, *session) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.tmpl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return testSession(t, &input{cfg: cfg, entry: path}, data)
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, string(b)
}

func put(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestHandlers(t *testing.T) {
	srv, _ := fileSession(t, `<p>{{ .name }}</p>`, gotp.Config{}, map[string]any{"name": "Gopher"})

	t.Run("index declares html", func(t *testing.T) {
		res, body := get(t, srv.URL+"/")
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q", ct)
		}
		if !strings.Contains(body, "<!DOCTYPE html>") {
			t.Error("index page not served")
		}
	})

	t.Run("out renders", func(t *testing.T) {
		res, body := get(t, srv.URL+"/out")
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
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
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
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
		if res, body := put(t, srv.URL+"/data", `{"name":"Rambo"}`); res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", res.StatusCode, body)
		}
		if _, body := get(t, srv.URL+"/out"); body != "<p>Rambo</p>" {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("bad data is rejected", func(t *testing.T) {
		if res, _ := put(t, srv.URL+"/data", `{oops`); res.StatusCode != http.StatusBadRequest {
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
// error text quotes the failing action as "<len .a>", so an unescaped message
// would inject a tag.
func TestOutRendersErrorsEscaped(t *testing.T) {
	srv, _ := fileSession(t, `{{ len .a }}`, gotp.Config{}, nil)
	_, body := get(t, srv.URL+"/out")
	if !strings.Contains(body, "<pre") {
		t.Fatalf("error not rendered as a block: %s", body)
	}
	if !strings.Contains(body, "&lt;len .a&gt;") || strings.Contains(body, "<len .a>") {
		t.Errorf("error message was not escaped: %s", body)
	}
}

func TestOutServesTextMode(t *testing.T) {
	srv, _ := fileSession(t, `{{ .v }}`, gotp.Config{Text: true}, map[string]any{"v": "a & b"})
	res, body := get(t, srv.URL+"/out")
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	if body != "a & b" {
		t.Errorf("body = %q", body)
	}
}

// Serving a directory lists every template and can switch between them.
func TestServeDirectory(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("_part.tmpl", `{{ define "part" }}[shared]{{ end }}`)
	write("alpha.tmpl", `{{ template "part" . }}alpha`)
	write("beta.tmpl", `beta`)

	in := &input{dir: dir}
	found, err := in.candidates()
	if err != nil {
		t.Fatal(err)
	}
	entry, err := pickDirEntry(in, found)
	if err != nil {
		t.Fatal(err)
	}
	in.entry = entry
	srv, s := testSession(t, in, nil)

	// The partial renders nothing, so it is skipped when choosing what to
	// show first, and a template it defines resolves from the sibling file.
	if filepath.Base(in.entry) != "alpha.tmpl" {
		t.Fatalf("entry = %q", in.entry)
	}
	if _, body := get(t, srv.URL+"/out"); body != "[shared]alpha" {
		t.Errorf("out = %q", body)
	}

	t.Run("templates lists all of them", func(t *testing.T) {
		_, body := get(t, srv.URL+"/templates")
		var resp struct {
			Data struct {
				Templates []string `json:"templates"`
				Entry     string   `json:"entry"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Data.Templates) != 3 {
			t.Errorf("templates = %q", resp.Data.Templates)
		}
		if resp.Data.Entry != in.entry {
			t.Errorf("entry = %q", resp.Data.Entry)
		}
	})

	t.Run("entry can be switched", func(t *testing.T) {
		beta := filepath.Join(dir, "beta.tmpl")
		if res, body := put(t, srv.URL+"/entry", `{"entry":"`+beta+`"}`); res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", res.StatusCode, body)
		}
		if _, got := s.get(); got != beta {
			t.Errorf("entry = %q", got)
		}
		if _, body := get(t, srv.URL+"/out"); body != "beta" {
			t.Errorf("out = %q", body)
		}
	})

	t.Run("a path outside the set is refused", func(t *testing.T) {
		for _, bad := range []string{"/etc/passwd", dir + "/../secret.tmpl", ""} {
			res, _ := put(t, srv.URL+"/entry", `{"entry":"`+bad+`"}`)
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("%q: status = %d, want 400", bad, res.StatusCode)
			}
		}
	})

	t.Run("templates added later show up", func(t *testing.T) {
		write("gamma.tmpl", `gamma`)
		_, body := get(t, srv.URL+"/templates")
		if !strings.Contains(body, "gamma.tmpl") {
			t.Errorf("new template missing: %s", body)
		}
	})
}

// The reload stream must deliver a nudge to a connected browser.
func TestEventsStream(t *testing.T) {
	srv, s := fileSession(t, `x`, gotp.Config{}, nil)

	res, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}

	lines := make(chan string, 1)
	go func() {
		r := bufio.NewReader(res.Body)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data:") {
				lines <- strings.TrimSpace(line)
				return
			}
		}
	}()

	// Give the handler a moment to subscribe before publishing.
	deadline := time.After(3 * time.Second)
	for {
		s.broker.publish()
		select {
		case got := <-lines:
			if got != "data: -" {
				t.Errorf("event = %q", got)
			}
			return
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatal("no event received")
		}
	}
}

func TestBrokerUnsubscribe(t *testing.T) {
	b := newBroker()
	c := b.subscribe()
	b.publish()
	select {
	case <-c:
	default:
		t.Fatal("subscriber got no event")
	}

	b.unsubscribe(c)
	b.publish()
	select {
	case <-c:
		t.Fatal("unsubscribed client still got an event")
	default:
	}
}

// The data behind the session is read by requests while the UI replaces it.
// Run with -race.
func TestSessionConcurrent(t *testing.T) {
	s := &session{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func(i int) { defer wg.Done(); s.setData(map[string]any{"n": i}) }(i)
		go func() { defer wg.Done(); s.setEntry("x.tmpl") }()
		go func() { defer wg.Done(); _, _ = s.get() }()
	}
	wg.Wait()
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

	// Two saves in a row: the second is the one a per-file watch misses,
	// because the first rename replaced the watched file.
	for i, body := range []string{"two", "three"} {
		save(body)
		select {
		case <-changed:
		case <-time.After(3 * time.Second):
			t.Fatalf("no change reported for save %d", i+1)
		}
	}
}
