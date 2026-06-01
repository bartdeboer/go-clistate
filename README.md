# go-clistate

`go-clistate` is a small JSON-backed state/config store for Go command-line
apps. It keeps a local `<name>.json` file, preserves existing JSON layout when
updating values, and supports layered `config.d` defaults.

## Install

```bash
go get github.com/bartdeboer/go-clistate
```

## Basic usage

```go
package main

import "github.com/bartdeboer/go-clistate"

func main() {
    store, err := clistate.NewCwd("myapp", "config")
    if err != nil {
        panic(err)
    }

    name := store.GetString("user.name", "anonymous")
    _ = name

    if err := store.PersistString("user.name", "Bart"); err != nil {
        panic(err)
    }
}
```

`NewCwd("myapp", "config")` stores local config at:

```text
./.myapp/config.json
```

and also uses a matching global store as a parent fallback when available:

```text
~/.myapp/config.json
```

## Paths

Keys use dot notation:

```go
store.GetString("git.user_name", "")
```

Literal path segments can be quoted with brackets:

```go
store.PersistString(`chats["00abc"].provider_chat_title`, "Codex")
```

## Typed getters

```go
store.GetString("name", "default")
store.GetInt("count", 0)
store.GetFloat("ratio", 1.0)
store.GetBool("enabled", false)
store.GetStruct("workspaces", &out)
```

Source-aware variants are available when diagnostics matter:

```go
resolved := store.ResolveString("name", "default")
if resolved.Err != nil {
    panic(resolved.Err)
}
fmt.Println(resolved.Value, resolved.Source.String())
```

## Layered config.d defaults

For a base file:

```text
.myapp/config.json
```

`go-clistate` also reads JSON layers from:

```text
.myapp/config.d/*.json
```

The effective config is built once on load from low priority to high priority:

1. parent/global store
2. `config.d/*.json` in lexicographic order
3. `config.json`
4. explicit override pointers passed to getters

This means `config.d` is useful for generated or agent-managed defaults, while
human-owned `config.json` wins by default.

Merge rules:

- objects merge recursively
- scalars replace
- arrays replace
- node source/provenance is tracked where practical

Example:

```text
.myapp/config.d/10-defaults.json
.myapp/config.json
```

If `10-defaults.json` contains:

```json
{
  "server": {
    "host": "127.0.0.1",
    "port": 8080
  }
}
```

and `config.json` contains:

```json
{
  "server": {
    "port": 9090
  }
}
```

then:

```go
var server struct {
    Host string `json:"host"`
    Port int    `json:"port"`
}
store.GetStruct("server", &server)
// server.Host == "127.0.0.1"
// server.Port == 9090
```

## Writing layers

Normal `Persist*` methods write to the main `<name>.json` file:

```go
store.PersistString("server.port", "9090")
```

Overlay writes target `<name>.d/<layer>.json` explicitly:

```go
store.PersistOverlayString("10-defaults", "server.host", "127.0.0.1")
store.PersistOverlayStruct("10-defaults", "server", serverDefaults)
store.UnsetOverlay("10-defaults", "server.host")
```

Layer names are validated and normalized to `.json`.

## License

MIT
