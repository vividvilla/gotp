package gotp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// fields parses src and returns the discovered fields.
func fields(t *testing.T, cfg Config, src string) []string {
	t.Helper()
	tmpl, err := New(cfg, "test", []byte(src), nil)
	if err != nil {
		t.Fatalf("New(%q): %v", src, err)
	}
	return tmpl.Fields()
}

func TestFields(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"plain", `Hi {{ .name }}`, []string{".name"}},
		{"nested", `{{ .a.b.c }}`, []string{".a.b.c"}},
		{"pipeline", `{{ .x | printf "%s" }}`, []string{".x"}},
		{"if body", `{{ if .flag }}{{ .hidden }}{{ end }}`, []string{".flag", ".hidden"}},
		{"else body", `{{ if .a }}x{{ else }}{{ .b }}{{ end }}`, []string{".a", ".b"}},
		{"range scope", `{{ range .items }}{{ .name }}{{ end }}`, []string{".items", ".items[].name"}},
		{"with scope", `{{ with .user }}{{ .email }}{{ end }}`, []string{".user", ".user.email"}},
		{
			"nested range",
			`{{ range .a }}{{ range .b }}{{ .c }}{{ end }}{{ end }}`,
			[]string{".a", ".a[].b", ".a[].b[].c"},
		},
		{
			"dot inside range keeps outer scope for siblings",
			`{{ range .rows }}{{ . }}{{ end }}{{ .total }}`,
			[]string{".rows", ".total"},
		},
		{
			"unresolvable scope is marked",
			`{{ with printf "%s" .x }}{{ .y }}{{ end }}`,
			[]string{"*.y", ".x"},
		},
		{
			"variables are not followed",
			`{{ $x := .a }}{{ $x.b }}`,
			[]string{".a"},
		},
		{"no fields", `just text`, []string{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fields(t, Config{}, tc.src)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFieldsFollowsBaseTemplates(t *testing.T) {
	tmpl, err := New(Config{}, "testdata/page.tmpl", nil, []string{"testdata/base-*.tmpl"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		".meta",          // passed to the footer
		".meta.year",     // read inside the footer
		".posts",         //
		".posts[].title", // inside the range
		".posts[].url",   //
		".site.title",    // header, called with .
		".user.name",     //
	}
	if got := tmpl.Fields(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFieldsRecursiveIncludeTerminates(t *testing.T) {
	// A template that includes itself must not loop forever.
	src := `{{ define "loop" }}{{ .a }}{{ template "loop" . }}{{ end }}{{ template "loop" . }}`
	if got := fields(t, Config{}, src); !reflect.DeepEqual(got, []string{".a"}) {
		t.Errorf("got %q", got)
	}
}

func TestSkeleton(t *testing.T) {
	got := Skeleton([]string{
		".name",
		".user.email",
		".posts",
		".posts[].title",
		".posts[].author.name",
		"*.ignored",
	})
	want := map[string]any{
		"name": "",
		"user": map[string]any{"email": ""},
		"posts": []any{map[string]any{
			"title":  "",
			"author": map[string]any{"name": ""},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		t.Errorf("got %s, want %s", g, w)
	}
}

func TestSkeletonIsValidJSON(t *testing.T) {
	b, err := json.Marshal(Skeleton([]string{".a[].b", ".a[].c", ".d"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"a":[{"b":"","c":""}],"d":""}` {
		t.Errorf("got %s", got)
	}
}

func TestHTMLEscaping(t *testing.T) {
	const src = `{{ .v }}`
	data := map[string]any{"v": `Tom & Jerry <b>`}

	out, err := CompileString(Config{}, src, nil, data)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "Tom &amp; Jerry &lt;b&gt;" {
		t.Errorf("html mode: got %q", got)
	}

	out, err = CompileString(Config{Text: true}, src, nil, data)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != `Tom & Jerry <b>` {
		t.Errorf("text mode: got %q", got)
	}
}

func TestStubFuncs(t *testing.T) {
	// An app-specific function with no data behind it renders as a
	// placeholder rather than failing to parse.
	out, err := CompileString(Config{StubFuncs: true, Text: true}, `Dear {{ nominee_name }},`, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "Dear «nominee_name»," {
		t.Errorf("got %q", got)
	}

	// The same function can be mocked from the template data.
	out, err = CompileString(Config{StubFuncs: true, Text: true}, `Dear {{ nominee_name }},`, nil,
		map[string]any{"nominee_name": "Gopher"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "Dear Gopher," {
		t.Errorf("got %q", got)
	}

	// Stubs stay transparent in a pipeline.
	out, err = CompileString(Config{StubFuncs: true, Text: true}, `{{ .x | myfilter }}`, nil,
		map[string]any{"x": "kept"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "kept" {
		t.Errorf("got %q", got)
	}

	// More than one unknown function gets stubbed.
	out, err = CompileString(Config{StubFuncs: true, Text: true}, `{{ one }}{{ two }}`, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "«one»«two»" {
		t.Errorf("got %q", got)
	}
}

func TestUndefinedFuncWithoutStubsFails(t *testing.T) {
	_, err := CompileString(Config{}, `{{ nominee_name }}`, nil, nil)
	if err == nil {
		t.Fatal("expected an error for an undefined function")
	}
	if !strings.Contains(err.Error(), "not defined") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStubsDoNotHideRealParseErrors(t *testing.T) {
	// A syntax error is not a missing function, so stubbing must give up.
	if _, err := CompileString(Config{StubFuncs: true}, `{{ if .a }}`, nil, nil); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestTopLevelArrayData(t *testing.T) {
	out, err := CompileString(Config{Text: true}, `{{ range . }}{{ . }};{{ end }}`, nil, []any{1.0, 2.0})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "1;2;" {
		t.Errorf("got %q", got)
	}
}

func TestSprig(t *testing.T) {
	out, err := CompileString(Config{UseSprig: true, Text: true}, `{{ "x" | upper }}`, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "X" {
		t.Errorf("got %q", got)
	}
	if _, err := CompileString(Config{Text: true}, `{{ "x" | upper }}`, nil, nil); err == nil {
		t.Error("expected upper to be undefined without --sprig")
	}
}

func TestCompileUsesBaseTemplates(t *testing.T) {
	out, err := Compile(Config{}, "testdata/page.tmpl", []string{"testdata/base-*.tmpl"},
		map[string]any{
			"site": map[string]any{"title": "Site"},
			"user": map[string]any{"name": "Gopher"},
			"meta": map[string]any{"year": 2026},
		})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<h1>Site</h1>", "Hi Gopher", "<p>2026</p>"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestMissingTemplateFile(t *testing.T) {
	if _, err := Compile(Config{}, "testdata/nope.tmpl", nil, nil); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestWebUIEmbedded(t *testing.T) {
	if len(WebUI) == 0 {
		t.Fatal("web UI was not embedded")
	}
}
