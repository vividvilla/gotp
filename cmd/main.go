package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"
	"github.com/vividvilla/gotp"
)

// buildString is set at build time.
var buildString = "unknown"

// templateExts are the extensions treated as templates when a directory is
// served.
var templateExts = []string{".tmpl", ".tpl", ".gohtml", ".html"}

func main() {
	// Errors carrying an exit code are printed by the framework, and usage
	// errors are printed before Run returns, so there is nothing to report
	// here.
	if err := rootCmd().Run(context.Background(), rewriteStdinArg(os.Args)); err != nil {
		os.Exit(1)
	}
}

// valueFlags are the flags that take a following value, and so may legitimately
// be followed by a bare "-".
var valueFlags = []string{"-d", "--data", "-a", "--addr"}

// rewriteStdinArg turns a bare "-" argument into the --stdin flag. The argument
// parser reads "-" as the end of the flags and drops everything after it, which
// would silently ignore the rest of the command line.
func rewriteStdinArg(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i, a := range out {
		if a != "-" || i == 0 {
			continue
		}
		if slices.Contains(valueFlags, out[i-1]) {
			continue
		}
		out[i] = "--stdin"
	}
	return out
}

// out is where command output goes, which tests can replace.
func out(c *cli.Command) io.Writer {
	if w := c.Root().Writer; w != nil {
		return w
	}
	return os.Stdout
}

// in is where piped input is read from, which tests can replace.
func stdin(c *cli.Command) io.Reader {
	if r := c.Root().Reader; r != nil {
		return r
	}
	return os.Stdin
}

// fail returns a user-facing error with an exit code.
func fail(format string, args ...any) error {
	return cli.Exit("error: "+fmt.Sprintf(format, args...), 1)
}

func rootCmd() *cli.Command {
	return &cli.Command{
		Name:  "gotp",
		Usage: "Live preview Go templates with custom data",
		UsageText: `gotp [render] <template> [templates...]   Render a template to stdout
gotp serve <template|directory> [...]     Live preview in the browser
gotp fields <template> [templates...]     List the data fields a template reads
                                          (--json prints a data skeleton)

Every argument is a template, parsed together, and any of them may be a glob.
The one that renders is the first that is not made up only of {{ define }}
blocks, so a template and its partials can be given in any order. Use "-" to
read the template from stdin. render is the default: "gotp page.tmpl" renders it.`,
		Version:               buildString,
		DefaultCommand:        "render",
		EnableShellCompletion: true,
		// Flags declared here are available to every command, so that
		// they work with or without an explicit command name.
		Flags:    commonFlags(),
		Commands: []*cli.Command{renderCmd(), serveCmd(), fieldsCmd()},
	}
}

// commonFlags are the flags every command accepts.
func commonFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:      "data",
			Aliases:   []string{"d"},
			Usage:     "template data: inline `JSON`, @file.json, or @- for stdin",
			TakesFile: true,
		},
		&cli.BoolFlag{
			Name:  "stdin",
			Usage: "read the template from stdin; \"-\" as the template means the same",
		},
		&cli.BoolFlag{
			Name:  "text",
			Usage: "render as plain text, without HTML escaping",
		},
		&cli.BoolFlag{
			Name:  "sprig",
			Usage: "register the sprig function library",
		},
		&cli.BoolFlag{
			Name:  "stub-funcs",
			Usage: "stub out undefined functions instead of failing",
		},
		&cli.BoolFlag{
			Name:  "strict",
			Usage: "fail on data the template reads but does not have",
		},
	}
}

const argsUsage = "<template> [templates...]"

func renderCmd() *cli.Command {
	return &cli.Command{
		Name:      "render",
		Usage:     "Render a template to stdout",
		ArgsUsage: argsUsage,
		Action: func(_ context.Context, c *cli.Command) error {
			// Bare "gotp" lands here through the default command.
			if c.Args().Len() == 0 && !c.Bool("stdin") {
				return cli.ShowAppHelp(c)
			}
			in, err := resolveInput(c, false)
			if err != nil {
				return err
			}
			data, err := loadData(c, in.src != nil)
			if err != nil {
				return err
			}
			t, err := in.template(in.entry)
			if err != nil {
				return fail("%v", err)
			}
			rendered, err := t.Render(data)
			if err != nil {
				return fail("%v", err)
			}
			// Write the output as-is: a trailing newline the template
			// does not have would break comparing it against a file.
			_, err = out(c).Write(rendered)
			return err
		},
	}
}

func fieldsCmd() *cli.Command {
	return &cli.Command{
		Name:      "fields",
		Usage:     "List the data fields a template reads",
		ArgsUsage: argsUsage,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "json",
				Usage: "print a JSON data skeleton instead of one path per line",
				Local: true,
			},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			t, err := parseTarget(c)
			if err != nil {
				return err
			}
			fields := t.Fields()

			if !c.Bool("json") {
				for _, f := range fields {
					fmt.Fprintln(out(c), f)
				}
				return nil
			}

			b, err := json.MarshalIndent(gotp.Skeleton(fields), "", "  ")
			if err != nil {
				return fail("%v", err)
			}
			fmt.Fprintln(out(c), string(b))
			return nil
		},
	}
}

// parseTarget resolves and parses the template for the commands that only
// inspect it.
func parseTarget(c *cli.Command) (*gotp.Template, error) {
	in, err := resolveInput(c, false)
	if err != nil {
		return nil, err
	}
	t, err := in.template(in.entry)
	if err != nil {
		return nil, fail("%v", err)
	}
	return t, nil
}

// input is the template set a command works on: one entry template plus the
// patterns of everything parsed alongside it.
type input struct {
	cfg gotp.Config
	// entry is the path of the template being rendered. It is a label
	// rather than a path when src is set.
	entry string
	// src holds the template source when it was read from stdin.
	src []byte
	// args is every template argument, all parsed together so that a
	// partial defined in one of them resolves in another. It includes the
	// entry.
	args []string
	// dir is set when a directory is being served, in which case every
	// template in it is a candidate entry.
	dir string
}

// resolveInput turns the positional arguments into a template set. The first
// argument is the entry, "-" reads it from stdin, and the rest are parsed
// alongside it. Every argument may be a glob.
func resolveInput(c *cli.Command, allowDir bool) (*input, error) {
	args := c.Args().Slice()
	in := &input{
		cfg: gotp.Config{
			Text:      c.Bool("text"),
			UseSprig:  c.Bool("sprig"),
			StubFuncs: c.Bool("stub-funcs"),
			Strict:    c.Bool("strict"),
		},
	}

	// With --stdin the template comes from the pipe, so every argument is
	// a template parsed alongside it.
	if c.Bool("stdin") {
		b, err := io.ReadAll(stdin(c))
		if err != nil {
			return nil, fail("reading the template from stdin: %v", err)
		}
		in.src, in.entry, in.args = b, "stdin", args
		return in, nil
	}

	if len(args) == 0 {
		return nil, fail("no template given, see: gotp %s --help", c.Name)
	}
	in.args = args

	target := args[0]

	fi, err := os.Stat(target)
	if err != nil {
		// Not a plain path: it may be a glob the shell left alone.
		m, gerr := filepath.Glob(target)
		if gerr != nil || len(m) == 0 {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fail("no such template: %s", target)
			}
			return nil, fail("%v", err)
		}
		if len(m) > 1 {
			// A glob of a whole template set is not ambiguous: the
			// matches join the argument list and the entry is
			// chosen from them.
			in.args = append(m, args[1:]...)
			return in, in.chooseEntry(c)
		}
		in.args = append([]string{m[0]}, args[1:]...)
		return in, in.chooseEntry(c)
	}

	if fi.IsDir() {
		if !allowDir {
			return nil, fail("%s is a directory, %s needs a template file", target, c.Name)
		}
		in.dir = target
		// The directory itself is not a template: what is parsed are
		// the templates inside it, plus any further arguments.
		in.args = args[1:]
		found, err := in.candidates()
		if err != nil {
			return nil, err
		}
		if len(found) == 0 {
			return nil, fail("no templates (%s) in %s", strings.Join(templateExts, ", "), target)
		}
		entry, err := pickDirEntry(in, found)
		if err != nil {
			return nil, err
		}
		in.entry = entry
		return in, nil
	}

	return in, in.chooseEntry(c)
}

// chooseEntry decides which of the template arguments is rendered. It is the
// first one, unless that file holds only {{ define }} blocks and so renders
// nothing: naming one of those is the usual result of letting a glob or a
// listing decide the order of the arguments.
func (in *input) chooseEntry(c *cli.Command) error {
	first := in.args[0]

	// Report a problem with the template the user named first, rather than
	// quietly moving on to another one.
	t, err := in.template(first)
	if err != nil {
		return fail("%v", err)
	}
	if t.HasBody() {
		in.entry = first
		return nil
	}

	for _, p := range in.args[1:] {
		t, err := in.template(p)
		if err != nil || !t.HasBody() {
			continue
		}
		in.entry = p
		fmt.Fprintf(c.Root().ErrWriter, "gotp: %s only defines templates, rendering %s\n", first, p)
		return nil
	}

	return fail("%s only defines templates, so there is nothing to render.\n"+
		"Name the template that uses it first: gotp <template> %s", first, first)
}

// pickDirEntry chooses which template of a served directory to preview first:
// an index template if there is one, otherwise the first that renders
// something of its own. The web UI can switch to any of the others.
func pickDirEntry(in *input, found []string) (string, error) {
	ordered := make([]string, 0, len(found))
	for _, p := range found {
		if strings.HasPrefix(filepath.Base(p), "index.") {
			ordered = append(ordered, p)
		}
	}
	for _, p := range found {
		if !strings.HasPrefix(filepath.Base(p), "index.") {
			ordered = append(ordered, p)
		}
	}

	var firstErr error
	for _, p := range ordered {
		t, err := in.template(p)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if t.HasBody() {
			return p, nil
		}
	}

	// Nothing rendered. If that is because none of them even parsed, say
	// so rather than serving a blank page.
	if firstErr != nil {
		return "", fail("%v", firstErr)
	}
	return found[0], nil
}

// candidates lists the templates that may be previewed, sorted. It globs on
// every call so that templates added while the server runs show up.
func (in *input) candidates() ([]string, error) {
	if in.dir == "" {
		return []string{in.entry}, nil
	}
	var out []string
	for _, ext := range templateExts {
		m, err := filepath.Glob(filepath.Join(in.dir, "*"+ext))
		if err != nil {
			return nil, fail("%v", err)
		}
		out = append(out, m...)
	}
	slices.Sort(out)
	return out, nil
}

// previewable narrows the candidates to the templates that render something of
// their own, which are the only ones worth offering as a preview: a file made
// up of {{ define }} blocks shows a blank page.
func (in *input) previewable() ([]string, error) {
	found, err := in.candidates()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(found))
	for _, p := range found {
		if t, err := in.template(p); err == nil && t.HasBody() {
			out = append(out, p)
		}
	}
	return out, nil
}

// parsePaths returns the globs parsed alongside the entry. Patterns generated
// for a directory are dropped when they match nothing, while a pattern the
// user typed is left in so that a typo is reported rather than ignored.
func (in *input) parsePaths() []string {
	if in.dir == "" {
		return in.args
	}
	var out []string
	for _, ext := range templateExts {
		p := filepath.Join(in.dir, "*"+ext)
		if m, err := filepath.Glob(p); err == nil && len(m) > 0 {
			out = append(out, p)
		}
	}
	return append(out, in.args...)
}

// watchPaths returns the patterns whose changes should trigger a reload.
func (in *input) watchPaths() []string {
	return in.parsePaths()
}

// template parses the given entry along with everything parsed alongside it.
func (in *input) template(entry string) (*gotp.Template, error) {
	if in.src != nil {
		return gotp.New(in.cfg, "stdin", in.src, in.parsePaths())
	}
	return gotp.New(in.cfg, entry, nil, in.parsePaths())
}

// source returns the text of the given entry.
func (in *input) source(entry string) ([]byte, error) {
	if in.src != nil {
		return in.src, nil
	}
	return os.ReadFile(entry)
}

// loadData reads the --data value, which is inline JSON, @file.json, or @- for
// stdin.
func loadData(c *cli.Command, stdinTaken bool) (any, error) {
	arg := c.String("data")
	if arg == "" {
		return nil, nil
	}

	// A bare "-" is a common guess for reading stdin.
	if arg == "-" {
		return nil, fail("--data takes a file with @, use: -d @-")
	}

	raw := []byte(arg)
	if path, ok := strings.CutPrefix(arg, "@"); ok {
		if path == "-" {
			if stdinTaken {
				return nil, fail("cannot read both the template and the data from stdin")
			}
			b, err := io.ReadAll(stdin(c))
			if err != nil {
				return nil, fail("reading the data from stdin: %v", err)
			}
			raw = b
		} else {
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, fail("%v", err)
			}
			raw = b
		}
	}

	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fail("invalid template data: %v", err)
	}
	return data, nil
}
