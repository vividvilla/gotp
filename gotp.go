// Package gotp renders Go templates with mock data, for previewing them.
package gotp

import (
	"bytes"
	"embed"
	"fmt"
	htmltmpl "html/template"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	texttmpl "text/template"
	"text/template/parse"

	"github.com/Masterminds/sprig/v3"
)

// WebUI holds the page and assets served by the web mode.
//
//go:embed assets/index.html assets/app.js assets/oat.min.css
var WebUI embed.FS

// Config controls how templates are parsed and rendered.
type Config struct {
	// UseSprig registers the sprig function library.
	UseSprig bool
	// Text renders with text/template instead of html/template, so values
	// are not HTML-escaped. Use it for plain text, YAML, SQL and the like.
	Text bool
	// Strict fails rendering when the template reads a key the data does
	// not have, instead of rendering "<no value>". Note that this also
	// fails on "{{ if .maybe }}" tests for optional keys.
	Strict bool
	// StubFuncs registers a placeholder for every function the template
	// calls but that is not defined, so app-specific functions do not stop
	// the preview. A stub returns its first argument if given one (keeping
	// pipelines intact), a value from the template data under its own name
	// if there is one, and otherwise a visible «name» placeholder.
	StubFuncs bool
}

// undefinedFuncRe matches the parse error text/template reports for a call to
// an unregistered function. Errors are the only place the parser reveals them.
var undefinedFuncRe = regexp.MustCompile(`function "([^"]+)" not defined`)

// Template is a parsed template, backed by either html/template or
// text/template so that both engines can be driven through one code path.
type Template struct {
	html *htmltmpl.Template
	text *texttmpl.Template
	// data is what Execute was last called with. Stub functions read it so
	// that "{{ some_func }}" can be mocked from the same JSON as fields.
	data any
}

// New parses the named template. If src is nil the source is read from name as
// a file path, otherwise name is only used to label the template. Base
// templates are file paths or globs, parsed before the target.
func New(cfg Config, name string, src []byte, basePaths []string) (*Template, error) {
	if src == nil {
		b, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		src, name = b, filepath.Base(name)
	}

	t := &Template{}
	stubs := map[string]any{}
	for {
		err := t.parse(cfg, name, string(src), basePaths, stubs)
		if err == nil {
			return t, nil
		}
		if !cfg.StubFuncs {
			return nil, err
		}
		// Stub out the missing function the parser complained about and
		// retry. Each pass either stubs a name not stubbed before or
		// gives up, so this terminates.
		m := undefinedFuncRe.FindStringSubmatch(err.Error())
		if m == nil || stubs[m[1]] != nil {
			return nil, err
		}
		stubs[m[1]] = t.stub(m[1])
	}
}

// parse builds the underlying template for one attempt, with the given stubs
// registered.
func (t *Template) parse(cfg Config, name, src string, basePaths []string, stubs map[string]any) error {
	funcs := map[string]any{}
	if cfg.UseSprig {
		// GenericFuncMap suits both template engines.
		for k, v := range sprig.GenericFuncMap() {
			funcs[k] = v
		}
	}
	for k, v := range stubs {
		funcs[k] = v
	}

	// missingkey only has an effect on map lookups, which is what JSON
	// template data is made of.
	opt := "missingkey=default"
	if cfg.Strict {
		opt = "missingkey=error"
	}

	if cfg.Text {
		tt := texttmpl.New(name).Funcs(funcs).Option(opt)
		for _, b := range basePaths {
			if _, err := tt.ParseGlob(b); err != nil {
				return err
			}
		}
		if _, err := tt.Parse(src); err != nil {
			return err
		}
		if t.text = tt.Lookup(name); t.text == nil {
			return fmt.Errorf("template %q not found after parsing", name)
		}
		return nil
	}

	ht := htmltmpl.New(name).Funcs(funcs).Option(opt)
	for _, b := range basePaths {
		if _, err := ht.ParseGlob(b); err != nil {
			return err
		}
	}
	if _, err := ht.Parse(src); err != nil {
		return err
	}
	if t.html = ht.Lookup(name); t.html == nil {
		return fmt.Errorf("template %q not found after parsing", name)
	}
	return nil
}

// stub returns the placeholder function registered for name.
func (t *Template) stub(name string) any {
	return func(args ...any) any {
		if m, ok := t.data.(map[string]any); ok {
			if v, ok := m[name]; ok {
				return v
			}
		}
		// Keep pipelines transparent: "{{ .x | unknown }}" yields .x.
		if len(args) > 0 {
			return args[0]
		}
		return "«" + name + "»"
	}
}

// Execute renders the template into w with the given data.
func (t *Template) Execute(w io.Writer, data any) error {
	t.data = data
	if t.text != nil {
		return t.text.Execute(w, data)
	}
	return t.html.Execute(w, data)
}

// Render renders the template with the given data and returns the output.
func (t *Template) Render(data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// root returns the parse tree of the target template.
func (t *Template) root() *parse.Tree {
	if t.text != nil {
		return t.text.Tree
	}
	return t.html.Tree
}

// treeByName returns the parse tree of an associated template, or nil.
func (t *Template) treeByName(name string) *parse.Tree {
	if t.text != nil {
		if a := t.text.Lookup(name); a != nil {
			return a.Tree
		}
		return nil
	}
	if a := t.html.Lookup(name); a != nil {
		return a.Tree
	}
	return nil
}

// HasBody reports whether the template renders anything of its own. A file
// holding only {{ define }} blocks does not: the blocks become separate
// templates for others to include, leaving this one with nothing but the
// whitespace around them.
func (t *Template) HasBody() bool {
	tree := t.root()
	if tree == nil || tree.Root == nil {
		return false
	}
	for _, n := range tree.Root.Nodes {
		if txt, ok := n.(*parse.TextNode); ok && len(bytes.TrimSpace(txt.Text)) == 0 {
			continue
		}
		return true
	}
	return false
}

// Fields returns every data field the template reads, as dotted paths sorted
// for stable output. Paths are scoped by the blocks they appear in: a field
// inside "{{ range .items }}" reads as ".items[].name", and a scope the walker
// cannot resolve statically is marked with a "*" segment.
//
// ponytail: variables are not resolved, so fields reached through a range
// declaration ("{{ range $_, $v := .items }}{{ $v.name }}") are not reported.
// Tracking variable scopes needs a symbol table; add one if this bites.
func (t *Template) Fields() []string {
	tree := t.root()
	if tree == nil {
		return nil
	}
	w := &fieldWalker{t: t, found: map[string]bool{}, visited: map[string]bool{}}
	w.walk(tree.Root, "")

	out := make([]string, 0, len(w.found))
	for f := range w.found {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

type fieldWalker struct {
	t     *Template
	found map[string]bool
	// visited guards against templates that include each other.
	visited map[string]bool
}

func (w *fieldWalker) walk(n parse.Node, scope string) {
	switch n := n.(type) {
	case nil:
		return
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			w.walk(c, scope)
		}
	case *parse.ActionNode:
		w.walkPipe(n.Pipe, scope)
	case *parse.IfNode:
		w.walkPipe(n.Pipe, scope)
		w.walk(n.List, scope)
		w.walk(n.ElseList, scope)
	case *parse.WithNode:
		w.walkPipe(n.Pipe, scope)
		w.walk(n.List, w.scopeOf(n.Pipe, scope, ""))
		w.walk(n.ElseList, scope)
	case *parse.RangeNode:
		w.walkPipe(n.Pipe, scope)
		w.walk(n.List, w.scopeOf(n.Pipe, scope, "[]"))
		w.walk(n.ElseList, scope)
	case *parse.TemplateNode:
		w.walkPipe(n.Pipe, scope)
		// "{{ template "x" }}" passes no data, so nothing inside x can
		// resolve against this scope.
		if n.Pipe == nil || w.visited[n.Name] {
			return
		}
		sub := w.t.treeByName(n.Name)
		if sub == nil {
			return
		}
		w.visited[n.Name] = true
		w.walk(sub.Root, w.scopeOf(n.Pipe, scope, ""))
		delete(w.visited, n.Name)
	}
}

func (w *fieldWalker) walkPipe(p *parse.PipeNode, scope string) {
	if p == nil {
		return
	}
	for _, cmd := range p.Cmds {
		for _, arg := range cmd.Args {
			switch a := arg.(type) {
			case *parse.FieldNode:
				w.add(scope, a.Ident)
			case *parse.ChainNode:
				// Only ".x.y" style chains rooted at the current
				// scope tell us anything about the data.
				if _, ok := a.Node.(*parse.DotNode); ok {
					w.add(scope, a.Field)
				}
			case *parse.PipeNode:
				w.walkPipe(a, scope)
			}
		}
	}
}

func (w *fieldWalker) add(scope string, ident []string) {
	w.found[scope+"."+strings.Join(ident, ".")] = true
}

// scopeOf resolves the data scope that a range, with or template body sees.
// suffix marks the scope as a collection.
func (w *fieldWalker) scopeOf(p *parse.PipeNode, scope, suffix string) string {
	if p == nil || len(p.Cmds) != 1 || len(p.Cmds[0].Args) != 1 {
		return scope + "*" + suffix
	}
	switch a := p.Cmds[0].Args[0].(type) {
	case *parse.DotNode:
		return scope + suffix
	case *parse.FieldNode:
		return scope + "." + strings.Join(a.Ident, ".") + suffix
	}
	return scope + "*" + suffix
}

// Skeleton turns dotted field paths into a nested map, as a starting point for
// mock template data. Paths with an unresolved scope are skipped.
func Skeleton(fields []string) map[string]any {
	root := map[string]any{}
	for _, f := range fields {
		if strings.Contains(f, "*") {
			continue
		}
		segs := strings.Split(strings.TrimPrefix(f, "."), ".")
		cur := root
		for i, s := range segs {
			isList := strings.HasSuffix(s, "[]")
			key := strings.TrimSuffix(s, "[]")
			if key == "" {
				break
			}
			if i == len(segs)-1 && !isList {
				if _, ok := cur[key]; !ok {
					cur[key] = ""
				}
				break
			}
			next := childMap(cur, key, isList)
			if next == nil {
				break
			}
			cur = next
		}
	}
	return root
}

// childMap returns the map held at key, creating it (wrapped in a one-element
// list when isList) if it is absent or still a leaf placeholder. It returns nil
// when the existing shape disagrees, leaving the first shape in place.
func childMap(m map[string]any, key string, isList bool) map[string]any {
	switch v := m[key].(type) {
	case map[string]any:
		if isList {
			return nil
		}
		return v
	case []any:
		if !isList || len(v) == 0 {
			return nil
		}
		inner, _ := v[0].(map[string]any)
		return inner
	}
	inner := map[string]any{}
	if isList {
		m[key] = []any{inner}
	} else {
		m[key] = inner
	}
	return inner
}

// GetTemplate parses the template at tmpl along with its base templates.
func GetTemplate(cfg Config, tmpl string, basePaths []string) (*Template, error) {
	return New(cfg, tmpl, nil, basePaths)
}

// Compile renders the template at tmpl with the given data.
func Compile(cfg Config, tmpl string, basePaths []string, data any) ([]byte, error) {
	t, err := New(cfg, tmpl, nil, basePaths)
	if err != nil {
		return nil, err
	}
	return t.Render(data)
}

// CompileString renders template source with the given data.
func CompileString(cfg Config, src string, basePaths []string, data any) ([]byte, error) {
	t, err := New(cfg, "source", []byte(src), basePaths)
	if err != nil {
		return nil, err
	}
	return t.Render(data)
}
