package gotp

import (
	"bytes"
	"html/template"
	"path/filepath"
	"text/template/parse"

	"github.com/Masterminds/sprig"
)

type Config struct {
	UseSprig bool
}

// NodeFields returns list of fields evaluated in template.
func NodeFields(t *template.Template) []string {
	return listNodeFields(t.Tree.Root, nil)
}

func listNodeFields(node parse.Node, res []string) []string {
	if node.Type() == parse.NodeAction {
		res = append(res, node.String())
	}

	if ln, ok := node.(*parse.ListNode); ok {
		for _, n := range ln.Nodes {
			res = listNodeFields(n, res)
		}
	}

	return res
}

// GetTemplate returns a go template for given template paths.
func GetTemplate(cfg Config, tmpl string, baseTmplPaths []string) (*template.Template, error) {
	var err error
	t := template.New("main")

	if cfg.UseSprig {
		t = t.Funcs(sprig.FuncMap())
	}

	// Load base templates, if template name is glob pattern then it
	// loads all matches templates.
	for _, bt := range baseTmplPaths {
		t, err = t.ParseGlob(bt)
		if err != nil {
			return t, err
		}
	}

	// Load target template.
	t, err = t.ParseFiles(tmpl)
	if err != nil {
		return t, err
	}

	// From loaded templates get target template.
	// Loaded templates are referenced against the filename instead of full path,
	// so get the filename of the target template and load from registered templates.
	name := filepath.Base(tmpl)
	return t.Lookup(name), nil
}

// Compile compiles given template and base templates with given data.
func Compile(cfg Config, tmpl string, baseTmplPaths []string, data map[string]interface{}) ([]byte, error) {
	var err error
	t, err := GetTemplate(cfg, tmpl, baseTmplPaths)
	if err != nil {
		return []byte(""), err
	}

	var tmplBody bytes.Buffer
	if err := t.Execute(&tmplBody, data); err != nil {
		return []byte(""), err
	}
	return tmplBody.Bytes(), nil
}

// CompileString compiles template string with given data.
func CompileString(tmpl string, data map[string]interface{}) ([]byte, error) {
	var err error
	t := template.New("main")
	// Parse template string.
	t, err = t.Parse(tmpl)
	if err != nil {
		return []byte(""), err
	}
	var tmplBody bytes.Buffer
	// Execute template with given data.
	if err := t.Execute(&tmplBody, data); err != nil {
		return []byte(""), err
	}
	return tmplBody.Bytes(), nil
}
