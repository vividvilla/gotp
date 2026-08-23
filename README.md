<a href="https://zerodha.tech"><img src="https://zerodha.tech/static/images/github-badge.svg" align="right" /></a>

# Go Template Previewer

Simple utility to live edit Go template and preview with custom data.

![Screenshot](assets/screenshot.png "Gotp web mode")

## Installation

Download the latest release from the [releases page](https://github.com/vividvilla/gotp/releases), or clone this repository and run `make build`. The web UI is embedded in the binary, so the binary is all you need.

## Usage

```txt
gotp [render] <template> [templates...]   Render a template to stdout
gotp serve <template|directory> [...]     Live preview in the browser
gotp fields <template> [templates...]     List the data fields a template reads
```

Every argument is a template, parsed together with the others, so base
templates and partials are positional arguments rather than repeated flags. Any
of them may be a glob, whether the shell expands it or you quote it:

```bash
gotp page.tmpl partials/*.tmpl      # expanded by the shell
gotp page.tmpl 'partials/*.tmpl'    # expanded by gotp
```

Quoting is worth it under `serve`: the pattern is kept, so a template added to
`partials/` later triggers a reload too.

The template that gets rendered is the first argument, unless it holds only
`{{ define }}` blocks. Those render nothing on their own, so gotp skips to the
first argument that does and says which one it picked on stderr. Order therefore
does not matter when you pass a template and its partials in any arrangement:

```bash
gotp page.tmpl base-footer.tmpl     # both render page.tmpl
gotp base-footer.tmpl page.tmpl
gotp *.tmpl                         # order decided by the shell, still fine
```

Order only decides between arguments that each render something of their own, in
which case the first wins. Naming nothing but partials is an error that says so,
rather than printing an empty page.

`render` is the default, so `gotp page.tmpl` and `gotp render page.tmpl` are the
same. Use `-` as the template to read it from stdin.

### Flags

These apply to any command:

```txt
-d, --data JSON   template data: inline JSON, @file.json, or @- for stdin
    --text        render as plain text, without HTML escaping
    --sprig       register the sprig function library
    --stub-funcs  stub out undefined functions instead of failing
    --strict      fail on data the template reads but does not have
    --stdin       read the template from stdin, same as passing "-"
```

`fields` adds:

```txt
    --json           print a JSON data skeleton instead of one path per line
```

`serve` adds:

```txt
-a, --addr address   address to listen on (default "127.0.0.1:1111")
    --open           open the preview in the default browser
```

`--addr` takes a bare port (`--addr 9000`) and defaults to loopback, so the
preview is not exposed to the network. Pass `--addr :9000` to listen on every
interface.

### Examples

```bash
# Render to stdout.
gotp page.tmpl

# With base templates and data.
gotp page.tmpl base-*.tmpl -d '{"name": "Gopher"}'

# Scaffold a data file from what the template reads, then use it.
gotp fields --json page.tmpl base-*.tmpl > data.json
gotp render page.tmpl base-*.tmpl -d @data.json

# Data from a pipe.
kubectl get cm -o json | gotp page.tmpl -d @-

# Templates that are not HTML (plain text mail, YAML, SQL) want --text, so that
# values are not HTML-escaped.
gotp mail.tmpl --text -d '{"name": "Tom & Jerry"}'

# Read the template from stdin.
cat page.tmpl | gotp - --text

# Live preview, opening a browser.
gotp serve page.tmpl base-*.tmpl -d @data.json --open

# Live preview a whole directory, switching between templates in the UI.
gotp serve templates/ --addr 9000

# Check that a template still parses, for CI. No data needed.
gotp fields page.tmpl base-*.tmpl > /dev/null
```

## Example

`example/` is a receipt email, split the way real template sets are:

```txt
example/receipt.tmpl       the email being previewed
example/base-header.tmpl   {{ define "header" }}, the logo and sign-in button
example/base-rows.tmpl     {{ define "details" }} and {{ define "totals" }}
example/base-footer.tmpl   {{ define "footer" }}
example/data.json          data for all of it
```

```bash
# Render it. The partials can be named in any order, or not at all beyond
# the glob, since only receipt.tmpl renders anything by itself.
gotp --text example/receipt.tmpl example/base-*.tmpl -d @example/data.json

# Preview it live, with a picker for the four templates.
gotp serve example/ --text -d @example/data.json --open

# What data does it need? The answer matches example/data.json exactly.
gotp fields example/receipt.tmpl example/base-*.tmpl
```

The logo is an inline SVG rather than an `<img>`, so the example renders
identically with no network access.

Note the `--text`: this one needs it. Email HTML uses conditional comments like
`<!--[if mso]>` to reach Outlook, and `html/template` **elides comments**, so the
default mode would quietly drop them. It is worth knowing for any HTML email.

The field list shows what the discovery handles: `.items[].detail` is read inside
an `{{ if }}` inside a `{{ range }}`, and `.receipt.total` is read inside a
partial that is called as `{{ template "totals" .receipt }}`, so it is reported
against that scope rather than the root.

## Serving a directory

`gotp serve <directory>` parses every `.tmpl`, `.tpl`, `.gohtml` and `.html`
file in it, so partials defined in sibling files resolve, and the web UI gets a
picker to switch between them. The first preview is an `index.*` template if
there is one, otherwise the first template that renders something of its own.
Partials are skipped by what they contain, not by how they are named.

## Custom template functions

Templates that call functions your application registers cannot be parsed on
their own, since the function is not defined. `--stub-funcs` registers a
placeholder for each of them so the template still previews:

- if the template data has a key matching the function name, the stub returns
  that value, so `{{ nominee_name }}` can be mocked with
  `-d '{"nominee_name": "Gopher"}'`
- inside a pipeline the stub passes its input through, so `{{ .date | myformat }}`
  renders `.date`
- otherwise it renders a visible `«name»` placeholder

For the functions from [Sprig](http://masterminds.github.io/sprig/), use
`--sprig` instead, which registers the real implementations.

## Discovered fields

`gotp fields` lists every field the template reads, including fields inside
`if`, `range`, `with` and base templates. Paths are scoped to where they are
read, so a field inside `{{ range .posts }}` reads as `.posts[].title`.
`gotp fields --json` turns the same list into a JSON data skeleton, which the
web UI also offers as **Copy as JSON data**.

Fields reached through a variable
(`{{ range $_, $post := .posts }}{{ $post.title }}`) are not reported, and a
scope that cannot be resolved statically is marked with a `*` segment.

## Development

```bash
make check   # go vet + go test -race
make build   # build ./gotp
```
