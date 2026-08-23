package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

func TestMain(m *testing.M) {
	// Errors built with cli.Exit would otherwise end the test binary, and
	// the framework reports them on its own writer.
	cli.OsExiter = func(int) {}
	cli.ErrWriter = io.Discard
	os.Exit(m.Run())
}

// runStreams executes the CLI and returns its output and error streams
// separately, so that a test can tell which one something was written to.
func runStreams(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := rootCmd()
	root.Writer = &out
	root.ErrWriter = &errOut
	root.Reader = strings.NewReader(stdin)
	err := root.Run(context.Background(), rewriteStdinArg(append([]string{"gotp"}, args...)))
	return out.String(), errOut.String(), err
}

// run executes the CLI and returns what it wrote to its output stream.
func run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	out, _, err := runStreams(t, stdin, args...)
	return out, err
}

// write puts a template in dir and returns its path.
func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// tmpl writes a template into a temporary directory and returns its path.
func tmpl(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRender(t *testing.T) {
	page := tmpl(t, "page.tmpl", `Hi {{ .name }}`)

	// No verb: render is the default command.
	got, err := run(t, "", page, "-d", `{"name":"Gopher"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hi Gopher" {
		t.Errorf("got %q", got)
	}

	// The explicit verb does the same.
	got, err = run(t, "", "render", page, "-d", `{"name":"Gopher"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hi Gopher" {
		t.Errorf("explicit verb: got %q", got)
	}
}

func TestRenderNoTrailingNewline(t *testing.T) {
	got, err := run(t, "", tmpl(t, "p.tmpl", `no-newline`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "no-newline" {
		t.Errorf("got %q, want no added newline", got)
	}
}

func TestEscapingModes(t *testing.T) {
	page := tmpl(t, "p.tmpl", `{{ .v }}`)
	data := `{"v":"Tom & Jerry <b>"}`

	got, err := run(t, "", page, "-d", data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Tom &amp; Jerry &lt;b&gt;" {
		t.Errorf("html: got %q", got)
	}

	got, err = run(t, "", page, "-d", data, "--text")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Tom & Jerry <b>" {
		t.Errorf("text: got %q", got)
	}
}

func TestFields(t *testing.T) {
	page := tmpl(t, "p.tmpl", `{{ .a }}{{ range .items }}{{ .name }}{{ end }}`)

	got, err := run(t, "", "fields", page)
	if err != nil {
		t.Fatal(err)
	}
	want := ".a\n.items\n.items[].name\n"
	if got != want {
		t.Errorf("fields: got %q, want %q", got, want)
	}

	// --json turns the same list into a data skeleton.
	got, err = run(t, "", "fields", "--json", page)
	if err != nil {
		t.Fatal(err)
	}
	var skeleton map[string]any
	if err := json.Unmarshal([]byte(got), &skeleton); err != nil {
		t.Fatalf("--json output is not JSON: %v: %s", err, got)
	}
	if _, ok := skeleton["items"].([]any); !ok {
		t.Errorf("--json: got %s", got)
	}
	if _, ok := skeleton["a"].(string); !ok {
		t.Errorf("--json: got %s", got)
	}

	// The flag belongs to fields, not to rendering.
	if _, err := run(t, "", page, "--json"); err == nil {
		t.Error("--json should not be accepted by render")
	}
}

func TestDataSources(t *testing.T) {
	page := tmpl(t, "p.tmpl", `{{ .name }}`)
	file := filepath.Join(filepath.Dir(page), "d.json")
	if err := os.WriteFile(file, []byte(`{"name":"FromFile"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		stdin string
		args  []string
		want  string
	}{
		{"inline", "", []string{page, "-d", `{"name":"Inline"}`}, "Inline"},
		{"file", "", []string{page, "-d", "@" + file}, "FromFile"},
		{"stdin", `{"name":"FromStdin"}`, []string{page, "-d", "@-"}, "FromStdin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := run(t, tc.stdin, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A bare "-" ends flag parsing in the argument parser, so it is rewritten
// before dispatch. Without that, the flags after it are silently dropped.
func TestStdinTemplateKeepsLaterFlags(t *testing.T) {
	got, err := run(t, `text: {{ .x }}`, "-", "--text", "-d", `{"x":"kept"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "text: kept" {
		t.Errorf("got %q", got)
	}

	// The explicit flag spelling behaves the same.
	got, err = run(t, `text: {{ .x }}`, "--stdin", "--text", "-d", `{"x":"kept"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "text: kept" {
		t.Errorf("--stdin: got %q", got)
	}
}

func TestRewriteStdinArg(t *testing.T) {
	tests := []struct {
		in   []string
		want []string
	}{
		{[]string{"gotp", "-"}, []string{"gotp", "--stdin"}},
		{[]string{"gotp", "-", "--text"}, []string{"gotp", "--stdin", "--text"}},
		// A "-" that is the value of a flag must be left alone.
		{[]string{"gotp", "p.tmpl", "-d", "-"}, []string{"gotp", "p.tmpl", "-d", "-"}},
		{[]string{"gotp", "serve", "--addr", "-"}, []string{"gotp", "serve", "--addr", "-"}},
		{[]string{"gotp", "p.tmpl"}, []string{"gotp", "p.tmpl"}},
	}
	for _, tc := range tests {
		if got := rewriteStdinArg(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("rewriteStdinArg(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestErrors(t *testing.T) {
	page := tmpl(t, "p.tmpl", `{{ .a }}`)
	dir := filepath.Dir(page)

	tests := []struct {
		name  string
		stdin string
		args  []string
		want  string
	}{
		{"missing template", "", []string{"nope.tmpl"}, "no such template"},
		{"bad json", "", []string{page, "-d", "{oops"}, "invalid template data"},
		{"missing data file", "", []string{page, "-d", "@nope.json"}, "no such file"},
		{"bare dash data", "", []string{page, "-d", "-"}, "-d @-"},
		{"both stdin", "x", []string{"-", "-d", "@-"}, "both the template and the data"},
		{"dir needs a file", "", []string{"fields", dir}, "is a directory"},
		// A flag that only applies to serve must not be silently ignored.
		{"serve flag on render", "", []string{page, "--addr", "9000"}, "not defined"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, err := runStreams(t, tc.stdin, tc.args...)
			if err == nil {
				t.Fatalf("expected an error, got output %q", out)
			}
			if got := err.Error() + errOut; !strings.Contains(got, tc.want) {
				t.Errorf("error %q does not mention %q", got, tc.want)
			}
		})
	}
}

func TestGlobEntry(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.tmpl", "x")
	write(t, dir, "_part.tmpl", `{{ define "p" }}p{{ end }}`)

	// One match resolves.
	got, err := run(t, "", filepath.Join(dir, "a*.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "x" {
		t.Errorf("got %q", got)
	}

	// A glob of the whole set joins the argument list, and the entry is the
	// one that renders something.
	got, err = run(t, "", filepath.Join(dir, "*.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "x" {
		t.Errorf("glob of the set: got %q", got)
	}
}

// The entry does not have to be named first. A file holding only {{ define }}
// blocks renders nothing, so it is skipped rather than silently producing an
// empty page.
func TestEntryOrderDoesNotMatter(t *testing.T) {
	dir := t.TempDir()
	page := write(t, dir, "page.tmpl", `{{ template "foot" . }}|body`)
	foot := write(t, dir, "base-foot.tmpl", `{{ define "foot" }}FOOT{{ end }}`)

	for _, args := range [][]string{{page, foot}, {foot, page}} {
		got, stderr, err := runStreams(t, "", args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		// Whatever the order, stdout is only the rendered template, so
		// that piping it stays usable.
		if got != "FOOT|body" {
			t.Errorf("%v: stdout = %q", args, got)
		}
		// Choosing a different template than the one named first is
		// reported, on the error stream.
		if args[0] == foot && !strings.Contains(stderr, "only defines templates") {
			t.Errorf("%v: expected a note on stderr, got %q", args, stderr)
		}
		if args[0] == page && stderr != "" {
			t.Errorf("%v: unexpected note %q", args, stderr)
		}
	}

	// Naming only a partial says so, instead of rendering nothing.
	out, err := run(t, "", foot)
	if err == nil {
		t.Fatalf("expected an error, got %q", out)
	}
	if !strings.Contains(err.Error(), "only defines templates") {
		t.Errorf("error = %v", err)
	}

	// When two templates both render, the first one still wins.
	other := write(t, dir, "other.tmpl", `OTHER`)
	got, err := run(t, "", other, page)
	if err != nil {
		t.Fatal(err)
	}
	if got != "OTHER" {
		t.Errorf("got %q, want the first argument to win", got)
	}
}

// A broken template named first is reported, not skipped over in favour of
// another argument.
func TestBrokenEntryIsReported(t *testing.T) {
	dir := t.TempDir()
	broken := write(t, dir, "broken.tmpl", `{{ if .x }}`)
	write(t, dir, "fine.tmpl", `fine`)

	if _, err := run(t, "", broken, filepath.Join(dir, "fine.tmpl")); err == nil {
		t.Fatal("expected the parse error to be reported")
	}
}

func TestIncludes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	page := write("page.tmpl", `{{ template "head" . }}|body`)
	write("base-head.tmpl", `{{ define "head" }}HEAD{{ end }}`)

	// As a glob the shell left alone.
	got, err := run(t, "", page, filepath.Join(dir, "base-*.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "HEAD|body" {
		t.Errorf("glob include: got %q", got)
	}

	// As separate arguments, the way a shell expands them.
	got, err = run(t, "", page, filepath.Join(dir, "base-head.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "HEAD|body" {
		t.Errorf("expanded include: got %q", got)
	}
}

func TestStrictFlag(t *testing.T) {
	page := tmpl(t, "p.tmpl", `{{ .missing }}`)

	if _, err := run(t, "", page, "-d", `{}`); err != nil {
		t.Fatalf("without --strict: %v", err)
	}
	if _, err := run(t, "", page, "-d", `{}`, "--strict"); err == nil {
		t.Error("with --strict: expected an error for the missing key")
	}
}

func TestPickDirEntry(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			"index wins",
			map[string]string{
				"_p.tmpl":    `{{ define "p" }}p{{ end }}`,
				"alpha.tmpl": `alpha`,
				"index.tmpl": `index`,
			},
			"index.tmpl",
		},
		{
			"templates that render nothing are skipped",
			map[string]string{
				"_p.tmpl":    `{{ define "p" }}p{{ end }}`,
				"alpha.tmpl": `alpha`,
			},
			"alpha.tmpl",
		},
		{
			// A partial named without the underscore convention is
			// still skipped, because it renders nothing.
			"detection does not rely on the file name",
			map[string]string{
				"aaa.tmpl": `{{ define "a" }}a{{ end }}`,
				"zzz.tmpl": `zzz`,
			},
			"zzz.tmpl",
		},
		{
			"nothing renders: the first is kept for the picker",
			map[string]string{
				"a.tmpl": `{{ define "a" }}a{{ end }}`,
				"b.tmpl": `{{ define "b" }}b{{ end }}`,
			},
			"a.tmpl",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				write(t, dir, name, body)
			}
			in := &input{dir: dir}
			found, err := in.candidates()
			if err != nil {
				t.Fatal(err)
			}
			if got := pickDirEntry(in, found); filepath.Base(got) != tc.want {
				t.Errorf("got %q, want %q", filepath.Base(got), tc.want)
			}
		})
	}
}

func TestNormalizeAddr(t *testing.T) {
	tests := map[string]string{
		"":               "127.0.0.1:1111",
		"9000":           "127.0.0.1:9000",
		":9000":          ":9000",
		"localhost:9000": "localhost:9000",
		"0.0.0.0:80":     "0.0.0.0:80",
		"example":        "example:1111",
	}
	for in, want := range tests {
		if got := normalizeAddr(in); got != want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", in, got, want)
		}
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
