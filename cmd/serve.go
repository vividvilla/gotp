package main

import (
	"context"
	"fmt"
	"html"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/labstack/echo/v4"
	"github.com/urfave/cli/v3"
	"github.com/vividvilla/gotp"
)

const defaultPort = "1111"

func serveCmd() *cli.Command {
	return &cli.Command{
		Name:      "serve",
		Usage:     "Live preview a template, or a directory of templates, in the browser",
		ArgsUsage: "<template|directory> [templates...]",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "addr",
				Aliases: []string{"a"},
				Value:   "127.0.0.1:" + defaultPort,
				Usage:   "`address` to listen on; a bare port means loopback, \":port\" means every interface",
				Local:   true,
			},
			&cli.BoolFlag{
				Name:  "open",
				Usage: "open the preview in the default browser",
				Local: true,
			},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			in, err := resolveInput(c, true)
			if err != nil {
				return err
			}
			data, err := loadData(c, in.src != nil)
			if err != nil {
				return err
			}
			return serve(in, data, normalizeAddr(c.String("addr")), c.Bool("open"))
		},
	}
}

// normalizeAddr accepts a bare port or host, defaulting to loopback so that a
// preview is not exposed to the network unless ":port" or a host is given.
func normalizeAddr(addr string) string {
	if addr == "" {
		return "127.0.0.1:" + defaultPort
	}
	if strings.Contains(addr, ":") {
		return addr
	}
	if _, err := strconv.Atoi(addr); err == nil {
		return "127.0.0.1:" + addr
	}
	return addr + ":" + defaultPort
}

// session is the mutable state of a serve run: the data the UI edits, and
// which template is being previewed when a directory is served.
type session struct {
	in     *input
	broker *broker

	mu    sync.RWMutex
	data  any
	entry string
}

func (s *session) get() (any, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data, s.entry
}

func (s *session) setData(v any) {
	s.mu.Lock()
	s.data = v
	s.mu.Unlock()
}

func (s *session) setEntry(v string) {
	s.mu.Lock()
	s.entry = v
	s.mu.Unlock()
}

// resp is the response envelope the web UI reads.
type resp struct {
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// fieldsPayload is the field list plus a data skeleton built from it.
func fieldsPayload(t *gotp.Template) map[string]any {
	f := t.Fields()
	return map[string]any{"fields": f, "skeleton": gotp.Skeleton(f)}
}

// render parses and renders the given entry.
func (s *session) render(data any, entry string) ([]byte, error) {
	t, err := s.in.template(entry)
	if err != nil {
		return nil, err
	}
	return t.Render(data)
}

func newServer(s *session) *echo.Echo {
	e := echo.New()
	e.HideBanner, e.HidePort = true, true

	e.GET("/", asset("index.html", echo.MIMETextHTMLCharsetUTF8))
	e.GET("/app.js", asset("app.js", "text/javascript; charset=utf-8"))
	e.GET("/oat.min.css", asset("oat.min.css", "text/css; charset=utf-8"))

	e.GET("/events", s.events)

	// Rendered output, loaded in the preview iframe.
	e.GET("/out", func(c echo.Context) error {
		data, entry := s.get()
		out, err := s.render(data, entry)
		if err != nil {
			// Errors belong in the preview pane, escaped so that a
			// message containing markup cannot break the page.
			return c.HTMLBlob(http.StatusOK, errorPage(err))
		}
		return c.HTMLBlob(http.StatusOK, out)
	})

	// The template being previewed, shown in the UI.
	e.GET("/source", func(c echo.Context) error {
		_, entry := s.get()
		src, err := s.in.source(entry)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, resp{Error: err.Error()})
		}
		return c.Blob(http.StatusOK, echo.MIMETextPlainCharsetUTF8, src)
	})

	e.GET("/data", func(c echo.Context) error {
		data, _ := s.get()
		return c.JSON(http.StatusOK, resp{Data: data})
	})

	e.PUT("/data", func(c echo.Context) error {
		var data any
		if err := c.Bind(&data); err != nil {
			return c.JSON(http.StatusBadRequest, resp{Error: fmt.Sprintf("Error parsing JSON data: %v", err)})
		}
		s.setData(data)
		s.broker.publish()
		return c.JSON(http.StatusOK, resp{})
	})

	e.GET("/fields", func(c echo.Context) error {
		_, entry := s.get()
		t, err := s.in.template(entry)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, resp{Error: fmt.Sprintf("Error parsing template: %v", err)})
		}
		return c.JSON(http.StatusOK, resp{Data: fieldsPayload(t)})
	})

	// The templates that can be previewed, and which one is current.
	e.GET("/templates", func(c echo.Context) error {
		found, err := s.in.previewable()
		if err != nil {
			return c.JSON(http.StatusInternalServerError, resp{Error: err.Error()})
		}
		_, entry := s.get()
		return c.JSON(http.StatusOK, resp{Data: map[string]any{
			"templates": found,
			"entry":     entry,
		}})
	})

	e.PUT("/entry", func(c echo.Context) error {
		var body struct {
			Entry string `json:"entry"`
		}
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, resp{Error: err.Error()})
		}
		found, err := s.in.candidates()
		if err != nil {
			return c.JSON(http.StatusInternalServerError, resp{Error: err.Error()})
		}
		// Only templates already known are accepted, so that a request
		// cannot name an arbitrary path on disk.
		if !slices.Contains(found, body.Entry) {
			return c.JSON(http.StatusBadRequest, resp{Error: fmt.Sprintf("unknown template: %q", body.Entry)})
		}
		s.setEntry(body.Entry)
		s.broker.publish()
		return c.JSON(http.StatusOK, resp{})
	})

	return e
}

// asset serves one of the embedded UI files.
func asset(name, contentType string) echo.HandlerFunc {
	return func(c echo.Context) error {
		b, err := gotp.WebUI.ReadFile("assets/" + name)
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, name)
		}
		return c.Blob(http.StatusOK, contentType, b)
	}
}

// events streams reload notifications to the browser.
func (s *session) events(c echo.Context) error {
	w := c.Response()
	w.Header().Set(echo.HeaderContentType, "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set(echo.HeaderConnection, "keep-alive")
	w.WriteHeader(http.StatusOK)
	w.Flush()

	ch := s.broker.subscribe()
	defer s.broker.unsubscribe(ch)

	for {
		select {
		case <-ch:
			if _, err := fmt.Fprint(w, "data: -\n\n"); err != nil {
				return nil
			}
			w.Flush()
		case <-c.Request().Context().Done():
			return nil
		}
	}
}

func serve(in *input, data any, addr string, open bool) error {
	s := &session{in: in, broker: newBroker(), data: data, entry: in.entry}

	// Watch for edits, unless the template came from stdin and cannot
	// change.
	if in.src == nil {
		fw, err := watch(in.watchPaths(), s.broker.publish)
		if err != nil {
			return fail("watching files: %v", err)
		}
		defer fw.Close()
	}

	url := "http://" + addr
	if in.dir != "" {
		found, err := in.candidates()
		if err != nil {
			return err
		}
		log.Printf("serving %d templates from %s, starting with %s", len(found), in.dir, in.entry)
	}
	log.Printf("Starting server on %s", url)

	if open {
		// Give the listener a moment before handing the URL to a
		// browser.
		time.AfterFunc(300*time.Millisecond, func() { openBrowser(url) })
	}

	if err := newServer(s).Start(addr); err != nil && err != http.ErrServerClosed {
		return fail("%v", err)
	}
	return nil
}

// openBrowser asks the desktop to open url, and only reports a failure, since
// not being able to is not a reason to stop serving.
func openBrowser(url string) {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		name = "xdg-open"
	}
	if err := exec.Command(name, append(args, url)...).Start(); err != nil {
		log.Printf("could not open a browser: %v", err)
	}
}

// errorPage renders a template failure into the preview pane.
func errorPage(err error) []byte {
	return []byte(`<pre style="font-family: monospace; color: #b00; background: #fff4f4;` +
		`border: 1px solid #b00; border-radius: 5px; padding: 15px; white-space: pre-wrap">` +
		html.EscapeString(err.Error()) + `</pre>`)
}

// broker is a minimal server-sent-events fan-out. All the web UI needs is a
// nudge to reload.
type broker struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}

func newBroker() *broker {
	return &broker{clients: map[chan struct{}]struct{}{}}
}

func (b *broker) subscribe() chan struct{} {
	c := make(chan struct{}, 1)
	b.mu.Lock()
	b.clients[c] = struct{}{}
	b.mu.Unlock()
	return c
}

func (b *broker) unsubscribe(c chan struct{}) {
	b.mu.Lock()
	delete(b.clients, c)
	b.mu.Unlock()
}

func (b *broker) publish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.clients {
		// A client with a nudge already pending does not need another.
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// watch calls onChange when any file matching paths changes. It watches the
// containing directories rather than the files themselves: editors save by
// writing a new file and renaming it over the old one, which silently drops a
// watch registered on the original file.
func watch(paths []string, onChange func()) (*fsnotify.Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	dirs := map[string]struct{}{}
	for _, p := range paths {
		dirs[filepath.Dir(p)] = struct{}{}
	}
	for d := range dirs {
		if err := fw.Add(d); err != nil {
			fw.Close()
			return nil, err
		}
	}

	go func() {
		var t *time.Timer
		for {
			select {
			case ev, ok := <-fw.Events:
				if !ok {
					return
				}
				if !matches(paths, ev.Name) {
					continue
				}
				log.Printf("changed: %s", ev.Name)
				// One save fires several events; coalesce them
				// into a single reload.
				if t != nil {
					t.Stop()
				}
				t = time.AfterFunc(100*time.Millisecond, onChange)
			case err, ok := <-fw.Errors:
				if !ok {
					return
				}
				log.Printf("error watching files: %v", err)
			}
		}
	}()
	return fw, nil
}

// matches reports whether path is one of the given paths or glob patterns.
// Comparison is on the file name, since watched paths may be relative while
// events carry the directory they came from.
func matches(patterns []string, path string) bool {
	name := filepath.Base(path)
	for _, p := range patterns {
		if ok, _ := filepath.Match(filepath.Base(p), name); ok {
			return true
		}
	}
	return false
}
