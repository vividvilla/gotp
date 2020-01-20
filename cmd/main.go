package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/knadh/stuffbin"
	"github.com/r3labs/sse"
	flag "github.com/spf13/pflag"
	"github.com/vividvilla/gotp"
)

const helpTxt = `
Usage:
gotp sample.tmpl
gotp --base-tmpl base.tmpl sample.tmpl
gotp --base-tmpl base.tmpl sample.tmpl --data '{"name": "Gopher"}'
gotp --base-tmpl base/*.tmpl sample.tmpl
gotp --base-tmpl base.tmpl sample.tmpl
gotp --web sample.tmpl
gotp --web --addr :9000 sample.tmpl
gotp --web --base-tmpl base.tmpl sample.tmpl
`

var (
	outputModeWeb bool
	tmplData      map[string]interface{}
	tmplDataMux   sync.Mutex
	addr          string
	tmplPath      string
	baseTmplPaths []string
	server        *sse.Server
	buildString   = "unknown"
	nodeFieldRe   = regexp.MustCompile(`{{(?:\s+)?(\.[\w]+)(?:\s+)?}}`)
)

// Resp represents JSON response structure.
type Resp struct {
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

func init() {
	var (
		tmplDataRaw string
		version     bool
	)
	flag.StringVarP(&addr, "addr", "a", ":1111", "Address to start the server on.")
	flag.StringArrayVarP(&baseTmplPaths, "base-tmpl", "b", []string{}, "Base template, or glob pattern.")
	flag.StringVarP(&tmplDataRaw, "data", "d", "", "Template data.")
	flag.BoolVarP(&outputModeWeb, "web", "w", false, "Run web UI")
	flag.BoolVarP(&version, "version", "v", false, "Version info")

	// Usage help.
	flag.Usage = func() {
		fmt.Printf("Go template previewer - Live preview Go templates with custom data.\n")
		fmt.Println(helpTxt)
		flag.PrintDefaults()
	}

	// Parse flags.
	flag.Parse()
	if flag.NFlag() == 0 && len(flag.Args()) == 0 {
		flag.Usage()
		os.Exit(0)
		return
	}

	// Print version and exit
	if version {
		fmt.Printf("Gotp - %s\n", buildString)
		os.Exit(0)
	}

	// Assign target template path.
	tmplPath = flag.Args()[0]

	// Assign templateData
	if tmplDataRaw != "" {
		d, err := decodeTemplateData([]byte(tmplDataRaw))
		if err != nil {
			fmt.Printf("invalid template data: %v", err)
			os.Exit(1)
		}
		tmplData = d
	}
}

func initFileWatcher(paths []string) (*fsnotify.Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return fw, err
	}

	files := []string{}
	// Get all the files which needs to be watched.
	for _, p := range paths {
		m, err := filepath.Glob(p)
		if err != nil {
			return fw, err
		}
		files = append(files, m...)
	}

	// Watch all template files.
	for _, f := range files {
		if err := fw.Add(f); err != nil {
			return fw, err
		}
	}
	return fw, err
}

func initSSEServer(fw *fsnotify.Watcher) *sse.Server {
	server := sse.New()
	server.CreateStream("messages")
	go func() {
		for {
			select {
			// Watch for events.
			case _ = <-fw.Events:
				log.Printf("files changed")
				// Send a ping notify frontent about file changes.
				server.Publish("messages", &sse.Event{
					Data: []byte("-"),
				})
			// Watch for errors.
			case err := <-fw.Errors:
				log.Printf("error watching files: %v", err)
			}
		}
	}()
	return server
}

func binPath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return path, nil
}

func initFileSystem() (stuffbin.FileSystem, error) {
	path, err := binPath()
	if err != nil {
		return nil, fmt.Errorf("error getting binary path: %v", err.Error())
	}

	// Read stuffed data from self.
	fs, err := stuffbin.UnStuff(path)
	if err != nil {
		if err == stuffbin.ErrNoID {
			fs, err = stuffbin.NewLocalFS("/", "./", "../assets/index.html:/assets/index.html")
			if err != nil {
				return fs, fmt.Errorf("error falling back to local filesystem: %v", err)
			}
		} else {
			return fs, fmt.Errorf("error reading stuffed binary: %v", err)
		}
	}
	return fs, nil
}

func decodeTemplateData(dataRaw []byte) (map[string]interface{}, error) {
	var data map[string]interface{}
	err := json.Unmarshal(dataRaw, &data)
	if err != nil {
		return data, err
	}
	return data, nil
}

func writeJSONResp(w http.ResponseWriter, statusCode int, data []byte, e string) {
	var resp = Resp{
		Data:  json.RawMessage(data),
		Error: e,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		log.Printf("error encoding response: %v, data: %v", err, string(data))
	}
	w.WriteHeader(statusCode)
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func handleGetTemplateData(w http.ResponseWriter, r *http.Request) {
	d, err := json.Marshal(tmplData)
	if err != nil {
		writeJSONResp(w, http.StatusInternalServerError, nil, fmt.Sprintf("Error reading request body: %v", err))
	} else {
		writeJSONResp(w, http.StatusOK, d, "")
	}
}

func handleUpdateTemplateData(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		writeJSONResp(w, http.StatusBadRequest, nil, fmt.Sprintf("Error reading request body: %v", err))
		return
	}

	data, err := decodeTemplateData(body)
	if err != nil {
		writeJSONResp(w, http.StatusBadRequest, nil, fmt.Sprintf("Error parsing JSON data: %v", err))
		return
	}

	// Update template data.
	tmplDataMux.Lock()
	tmplData = data
	tmplDataMux.Unlock()

	// Publish a message to reload.
	server.Publish("messages", &sse.Event{
		Data: []byte("-"),
	})

	writeJSONResp(w, http.StatusOK, nil, "")
}

func handleGetTemplateFields(w http.ResponseWriter, r *http.Request) {
	t, err := gotp.GetTemplate(tmplPath, baseTmplPaths)
	if err != nil {
		writeJSONResp(w, http.StatusInternalServerError, nil, fmt.Sprintf("Error getting template fields: %v", err))
		return
	}
	fields := gotp.NodeFields(t)
	mFields := []string{}
	for _, f := range fields {
		if nodeFieldRe.MatchString(f) {
			mFields = append(mFields, f)
		}
	}
	b, err := json.Marshal(mFields)
	if err != nil {
		writeJSONResp(w, http.StatusInternalServerError, nil, fmt.Sprintf("Error encoding template fields: %v", err))
		return
	}
	writeJSONResp(w, http.StatusOK, b, "")
}

func main() {
	if !outputModeWeb {
		b, err := gotp.Compile(tmplPath, baseTmplPaths, tmplData)
		if err != nil {
			fmt.Printf("error rendering template: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}

	// Initialize file system.
	fs, err := initFileSystem()
	if err != nil {
		log.Printf("error initializing file system: %v", err)
		os.Exit(1)
	}

	// Initialize file watcher.
	paths := append(baseTmplPaths, tmplPath)
	fw, err := initFileWatcher(paths)
	defer fw.Close()
	if err != nil {
		log.Printf("error watching files: %v", err)
		os.Exit(1)
	}

	// Initialize SSE server.
	server = initSSEServer(fw)

	// Create a new Mux and set the handler
	mux := http.NewServeMux()
	// Attach SSE handler.
	mux.HandleFunc("/events", server.HTTPHandler)
	// Server index.html page.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f, err := fs.Get("/assets/index.html")
		if err != nil {
			log.Fatalf("error reading foo.txt: %v", err)
		}
		// Write response.
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "text/html")
		w.Write(f.ReadBytes())
	})

	// Handler to render template.
	mux.HandleFunc("/out", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			// Write response.
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Complie the template.
		b, err := gotp.Compile(tmplPath, baseTmplPaths, tmplData)
		// If error send error as output.
		if err != nil {
			b = []byte(fmt.Sprintf("error rendering: %v", err))
		}
		// Write response.
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "text/html")
		w.Write(b)
	})

	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			handleUpdateTemplateData(w, r)
			return
		} else if r.Method == http.MethodGet {
			handleGetTemplateData(w, r)
		} else {
			// Write response.
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/fields", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			handleGetTemplateFields(w, r)
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	log.Printf("Starting server on address - %v", addr)
	// Start server on given port.
	if err := http.ListenAndServe(addr, mux); err != nil {
		panic(err)
	}
}
