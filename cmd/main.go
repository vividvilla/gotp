package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	flag "github.com/spf13/pflag"
	"github.com/vividvilla/gotp"
)

const helpTxt = `
Usage:
gotp sample.tmpl
gotp --base-tmpl base.tmpl sample.tmpl
gotp --base-tmpl base.tmpl sample.tmpl --data '{"name": "Gopher"}'
gotp --base-tmpl base.tmpl sample.tmpl --data-file data.json
gotp --base-tmpl 'base/*.tmpl' sample.tmpl
gotp --text --stub-funcs mail.tmpl
cat sample.tmpl | gotp -
gotp --web sample.tmpl
gotp --web --addr :9000 sample.tmpl
gotp --web --base-tmpl base.tmpl sample.tmpl
`

// buildString is set at build time.
var buildString = "unknown"

// options is everything the flags produced.
type options struct {
	addr      string
	basePaths []string
	tmplPath  string
	// src holds the template source when it was read from stdin, in which
	// case tmplPath is not a file.
	src     []byte
	web     bool
	gotpCfg gotp.Config
}

// store holds the template data. The web UI replaces it while requests render
// with it, so every access goes through the lock.
type store struct {
	mu   sync.RWMutex
	data any
}

func (s *store) get() any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

func (s *store) set(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = v
}

// Resp represents JSON response structure.
type Resp struct {
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

func parseFlags(args []string) (*options, any, error) {
	var (
		opts     options
		dataRaw  string
		dataFile string
		version  bool
	)

	fs := flag.NewFlagSet("gotp", flag.ContinueOnError)
	fs.StringVarP(&opts.addr, "addr", "a", "127.0.0.1:1111", "Address to start the server on.")
	fs.StringArrayVarP(&opts.basePaths, "base-tmpl", "b", nil, "Base template, or glob pattern.")
	fs.StringVarP(&dataRaw, "data", "d", "", "Template data as JSON.")
	fs.StringVar(&dataFile, "data-file", "", "File containing template data as JSON.")
	fs.BoolVarP(&opts.web, "web", "w", false, "Run web UI")
	fs.BoolVarP(&version, "version", "v", false, "Version info")
	fs.BoolVar(&opts.gotpCfg.UseSprig, "sprig", false, "Enable sprig functions")
	fs.BoolVar(&opts.gotpCfg.Text, "text", false, "Render as plain text (no HTML escaping)")
	fs.BoolVar(&opts.gotpCfg.StubFuncs, "stub-funcs", false, "Stub out undefined template functions instead of failing")

	fs.Usage = func() {
		fmt.Print("Go template previewer - Live preview Go templates with custom data.\n")
		fmt.Print(helpTxt)
		fmt.Print(fs.FlagUsages())
	}

	if err := fs.Parse(args); err != nil {
		// pflag prints the usage itself for --help.
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	// Nothing to do at all: show usage.
	if fs.NFlag() == 0 && fs.NArg() == 0 {
		fs.Usage()
		return nil, nil, nil
	}

	if version {
		fmt.Printf("Gotp - %s\n", buildString)
		return nil, nil, nil
	}

	if fs.NArg() == 0 {
		return nil, nil, fmt.Errorf("no template given\nrun with --help for usage")
	}
	opts.tmplPath = fs.Arg(0)

	// "-" reads the template from stdin. It cannot be watched or re-read,
	// so hold on to the source.
	if opts.tmplPath == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, nil, fmt.Errorf("error reading template from stdin: %v", err)
		}
		opts.src = b
	}

	if dataRaw != "" && dataFile != "" {
		return nil, nil, fmt.Errorf("use either --data or --data-file, not both")
	}

	raw := []byte(dataRaw)
	if dataFile != "" {
		b, err := os.ReadFile(dataFile)
		if err != nil {
			return nil, nil, fmt.Errorf("error reading template data file: %v", err)
		}
		raw = b
	}

	var data any
	if len(raw) > 0 {
		d, err := decodeTemplateData(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid template data: %v", err)
		}
		data = d
	}
	return &opts, data, nil
}

func decodeTemplateData(raw []byte) (any, error) {
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

// compile renders the template with the given data, reading it fresh from disk
// so that edits show up without a restart.
func (o *options) compile(data any) ([]byte, error) {
	if o.src != nil {
		return gotp.CompileString(o.gotpCfg, string(o.src), o.basePaths, data)
	}
	return gotp.Compile(o.gotpCfg, o.tmplPath, o.basePaths, data)
}

func (o *options) template() (*gotp.Template, error) {
	return gotp.New(o.gotpCfg, o.tmplPath, o.src, o.basePaths)
}

// broker is a minimal server-sent-events fan-out. All the web UI needs is a
// nudge to reload, so this replaces a dependency.
type broker struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}

func newBroker() *broker {
	return &broker{clients: map[chan struct{}]struct{}{}}
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

func (b *broker) handler(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	c := make(chan struct{}, 1)
	b.mu.Lock()
	b.clients[c] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.clients, c)
		b.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	for {
		select {
		case <-c:
			fmt.Fprint(w, "data: -\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
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

func writeJSONResp(w http.ResponseWriter, statusCode int, data []byte, e string) {
	b, err := json.Marshal(Resp{Data: json.RawMessage(data), Error: e})
	if err != nil {
		log.Printf("error encoding response: %v, data: %v", err, string(data))
		b, statusCode = []byte(`{"error":"error encoding response"}`), http.StatusInternalServerError
	}
	// Headers have to be set before the status is written, or they are
	// dropped.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write(b)
}

// errorPage renders a template failure into the preview pane. The message is
// escaped so that a template error containing markup cannot break the page.
func errorPage(err error) []byte {
	return []byte(`<pre style="font-family: monospace; color: #b00; background: #fff4f4;` +
		`border: 1px solid #b00; border-radius: 5px; padding: 15px; white-space: pre-wrap">` +
		html.EscapeString(err.Error()) + `</pre>`)
}

// newMux wires up the web UI routes.
func newMux(opts *options, data *store, b *broker) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/events", b.handler)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write(gotp.WebUI)
	})

	// Rendered template output, loaded in the preview iframe.
	mux.HandleFunc("/out", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		out, err := opts.compile(data.get())
		if err != nil {
			out = errorPage(err)
		}
		contentType := "text/html"
		if opts.gotpCfg.Text {
			contentType = "text/plain; charset=utf-8"
			if err != nil {
				contentType = "text/html"
			}
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		w.Write(out)
	})

	// Template source, shown in the UI.
	mux.HandleFunc("/source", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		src := opts.src
		if src == nil {
			f, err := os.ReadFile(opts.tmplPath)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			src = f
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write(src)
	})

	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			d, err := json.Marshal(data.get())
			if err != nil {
				writeJSONResp(w, http.StatusInternalServerError, nil, fmt.Sprintf("Error encoding template data: %v", err))
				return
			}
			writeJSONResp(w, http.StatusOK, d, "")
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				writeJSONResp(w, http.StatusBadRequest, nil, fmt.Sprintf("Error reading request body: %v", err))
				return
			}
			d, err := decodeTemplateData(body)
			if err != nil {
				writeJSONResp(w, http.StatusBadRequest, nil, fmt.Sprintf("Error parsing JSON data: %v", err))
				return
			}
			data.set(d)
			b.publish()
			writeJSONResp(w, http.StatusOK, nil, "")
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	// Fields the template reads, plus a data skeleton built from them.
	mux.HandleFunc("/fields", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		t, err := opts.template()
		if err != nil {
			writeJSONResp(w, http.StatusInternalServerError, nil, fmt.Sprintf("Error parsing template: %v", err))
			return
		}
		fields := t.Fields()
		payload, err := json.Marshal(map[string]any{
			"fields":   fields,
			"skeleton": gotp.Skeleton(fields),
		})
		if err != nil {
			writeJSONResp(w, http.StatusInternalServerError, nil, fmt.Sprintf("Error encoding template fields: %v", err))
			return
		}
		writeJSONResp(w, http.StatusOK, payload, "")
	})

	return mux
}

func serve(opts *options, data *store) error {
	b := newBroker()

	// Watch the template and its base templates, unless the template came
	// from stdin and cannot change.
	if opts.src == nil {
		fw, err := watch(append(opts.basePaths, opts.tmplPath), b.publish)
		if err != nil {
			return fmt.Errorf("error watching files: %v", err)
		}
		defer fw.Close()
	}

	log.Printf("Starting server on http://%s", opts.addr)
	return http.ListenAndServe(opts.addr, newMux(opts, data, b))
}

func main() {
	opts, data, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	// Usage or version was printed.
	if opts == nil {
		return
	}

	if !opts.web {
		out, err := opts.compile(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error rendering template: %v\n", err)
			os.Exit(1)
		}
		// Write the output as-is; a trailing newline the template does
		// not have would break comparing the output against a file.
		os.Stdout.Write(out)
		return
	}

	s := &store{}
	s.set(data)
	if err := serve(opts, s); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
