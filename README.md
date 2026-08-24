<a href="https://zerodha.tech"><img src="https://zerodha.tech/static/images/github-badge.svg" align="right" /></a>

# gotp

Live preview and render Go templates with custom data, in the browser or on the command line.

- Single binary, self-contained
- Web UI with live reload
- Browse and render templates in a directory
- Automatic field discovery

![gotp serve](assets/screenshot.png)

## Usage

```sh
# Live preview in the browser, reloading on save
gotp serve page.tmpl partials/*.tmpl -d @data.json --open

# Serve a whole directory and switch templates in the UI
gotp serve templates/ --addr 9000

# Render to stdout instead
gotp page.tmpl

# With its partials, and some data
gotp page.tmpl partials/*.tmpl -d '{"name": "Gopher"}'

# Data from a file, or from a pipe
gotp page.tmpl -d @data.json
kubectl get cm -o json | gotp page.tmpl -d @-

# List the fields the template reads, one path per line
gotp fields page.tmpl partials/*.tmpl

# Scaffold the data file out of those fields, then use it
gotp fields --json page.tmpl partials/*.tmpl > data.json
gotp page.tmpl partials/*.tmpl -d @data.json

# Not HTML: plain text mail, YAML, SQL. Values are not escaped
gotp mail.tmpl --text -d '{"name": "Tom & Jerry"}'

# Templates that call functions your application registers
gotp mail.tmpl --stub-funcs

# Fail on missing data instead of rendering <no value>
gotp page.tmpl -d @data.json --strict

# Read the template from stdin
cat page.tmpl | gotp -

# Check that a template still parses, for CI. No data needed
gotp fields page.tmpl partials/*.tmpl > /dev/null
```

Run `gotp --help` for all options, or `gotp <command> --help` for one command.

## Install

### uvx, pipx, or pip

Run without installing:

```sh
uvx gotp page.tmpl
```

Or install it:

```sh
pipx install gotp
uv tool install gotp
pip install gotp  # inside a virtual environment
```

### npx or npm

```sh
npx @vividvilla/gotp page.tmpl   # run without installing
npm install --global @vividvilla/gotp
```

### Homebrew

```sh
brew install vividvilla/tap/gotp
```

### Standalone binary

Download an archive from [GitHub Releases](https://github.com/vividvilla/gotp/releases),
verify it against `SHA256SUMS`, and put `gotp` (`gotp.exe` on Windows) on your `PATH`.

Prebuilt binaries cover Linux x86-64/ARM64/ARM, macOS x86-64/Apple Silicon, and
Windows x86-64/ARM64. The web UI ships inside the binary, so there is nothing
else to install.

### From source

```sh
git clone https://github.com/vividvilla/gotp && cd gotp
make build
```

## Commands

```txt
gotp [render] <template> [templates...]   Render a template to stdout
gotp serve <template|directory> [...]     Live preview in the browser
gotp fields <template> [templates...]     List the data fields a template reads
```

`render` is the default, so `gotp page.tmpl` and `gotp render page.tmpl` are the
same thing.

### Arguments

Every argument is a template, and they all get parsed together. Base templates and
partials go in as arguments, not as repeated flags. Any argument can be a glob:

```sh
gotp page.tmpl partials/*.tmpl      # expanded by the shell
gotp page.tmpl 'partials/*.tmpl'    # expanded by gotp
```

Quote it under `serve` and gotp keeps the pattern, so a template you add to
`partials/` later triggers a reload too.

gotp renders the first argument, unless that file holds nothing but
`{{ define }}` blocks. A file like that renders nothing by itself, so gotp moves on
to the first argument that does, and prints on stderr which one it chose. Order
does not matter:

```sh
gotp page.tmpl base-footer.tmpl     # both render page.tmpl
gotp base-footer.tmpl page.tmpl
gotp *.tmpl                         # order decided by the shell, still fine
```

When two arguments both render something, the first one wins. Name nothing but
partials and you get an error saying so, not a blank page.

Use `-` as the template to read it from stdin.

### Flags

These apply to every command:

| Flag | |
| --- | --- |
| `-d, --data JSON` | Template data: inline JSON, `@file.json`, or `@-` for stdin |
| `--text` | Render with `text/template`, so values are not HTML-escaped |
| `--sprig` | Register the [Sprig](http://masterminds.github.io/sprig/) function library |
| `--stub-funcs` | Stub out functions the template calls but nothing defines |
| `--strict` | Fail on data the template reads but does not have |
| `--stdin` | Read the template from stdin, the same as passing `-` |

`fields` adds `--json`, which prints a JSON data skeleton instead of one path per
line. `serve` adds:

| Flag | |
| --- | --- |
| `-a, --addr address` | Address to listen on, default `127.0.0.1:1111` |
| `--open` | Open the preview in the default browser |

`--addr` takes a bare port (`--addr 9000`) and defaults to loopback, so a preview
is not exposed to the network. Pass `--addr :9000` to listen on every interface.

## The web UI

`gotp serve` opens two panes: the rendered template on the left, its data on the
right.

The left toolbar holds the template picker, a Preview/Source pair, and a width
toggle for checking a mobile layout. The size being rendered shows in the middle.
The gear sets the width the toggle uses.

The right panel is the data, as a flat list of one row per field path or as raw
JSON. gotp compares what the template reads against what the data has, and marks
a field the template reads but the data lacks as `unset`.

Editing a value keeps its JSON type, because a template calling
`printf "%.2f"` breaks on a string. Applying patches the data you gave instead of
replacing it, so keys the template never reads survive. If there is no data at
all, the panel offers to fill in the skeleton that `gotp fields --json` prints.

The theme follows the operating system, with a switch in the top bar.

Styling is [oat](https://github.com/knadh/oat) (MIT), vendored into `assets/` and
embedded with the rest of the UI. The rendered template loads in an iframe, so the
UI's own CSS cannot reach it.

## Serving a directory

`gotp serve <directory>` parses every `.tmpl`, `.tpl`, `.gohtml` and `.html` file
in it, so partials defined in sibling files resolve, and the UI gets a picker to
switch between them.

The picker only offers templates that render something, so a file of
`{{ define }}` blocks never shows up as a blank preview. It opens on an `index.*`
template if there is one, otherwise the first that renders. gotp works this out
from what a file contains, not from how it is named.

## Template functions

A template that calls a function your application registers will not parse on its
own, since nothing defines that function. `--stub-funcs` registers a placeholder
for each one so the template still previews:

- if the data has a key matching the function name, the stub returns that value,
  so `{{ nominee_name }}` can be mocked with `-d '{"nominee_name": "Gopher"}'`
- inside a pipeline the stub passes its input through, so `{{ .date | myformat }}`
  renders `.date`
- otherwise it renders a visible `«name»` placeholder

For the functions from [Sprig](http://masterminds.github.io/sprig/), `--sprig`
registers the real implementations instead.

## Discovered fields

`gotp fields` lists every field a template reads, including fields inside `if`,
`range` and `with`, and fields inside the partials it calls. Paths are scoped to
where they are read, so a field inside `{{ range .posts }}` reads as
`.posts[].title`, and a partial called as `{{ template "totals" .receipt }}`
reports its fields against `.receipt`.

The scoping catches a common slip: `{{ .url }}` written inside
`{{ with .request }}` reads `.request.url`, and the field list shows it.

Two limits. Fields reached through a variable
(`{{ range $_, $post := .posts }}{{ $post.title }}`) are not reported, since
resolving those needs a symbol table. A scope gotp cannot resolve statically gets
a `*` segment and stays out of the `--json` skeleton.

## Example

`example/` is two emails over one set of partials, the way real template sets are
built:

```txt
example/receipt.tmpl       a receipt: range, a conditional inside it, printf
example/otp.tmpl           a sign-in code: with, a nested scope
example/base-header.tmpl   {{ define "header" }}, the logo and sign-in button
example/base-rows.tmpl     {{ define "details" }} and {{ define "totals" }}
example/base-footer.tmpl   {{ define "footer" }}
example/data.json          data for both
```

```sh
# Render one. The partials can be named in any order
gotp --text example/receipt.tmpl example/base-*.tmpl -d @example/data.json

# Preview live, with a picker to switch between the two emails
gotp serve example/ --text -d @example/data.json --open

# What data does each one need? Both are covered by example/data.json
gotp fields example/receipt.tmpl example/base-*.tmpl
gotp fields example/otp.tmpl example/base-*.tmpl
```

This set needs the `--text`. Email HTML reaches Outlook through conditional
comments like `<!--[if mso]>`, and `html/template` strips comments, so the default
mode drops them silently. The same applies to any HTML email.

Both images are inline SVG, so the example renders the same with no network.

## Development

```sh
make check   # gofmt, go vet, go test -race
make build   # ./gotp
```

The web UI is `assets/index.html`, `assets/app.js` and a vendored copy of oat,
embedded with `go:embed`. Rebuild after changing it.

## License

MIT
