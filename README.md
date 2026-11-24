# gx

A native coding agent for the terminal. Go, standard library only.

`gx` opens an interactive session in the current workspace. `gx ask` runs one request and exits. The agent can read and edit files, search the tree, run shell commands, fetch public URLs, load skills, start subagents, and call MCP tools. Sessions and credentials live under `GX_HOME` or `~/.gx`.

## Install

```sh
go build -o gx ./cmd/gx
```

Requires Go 1.27.

## Use

```sh
gx
gx ask "Explain the changes in this repository"
gx login codex
gx status
```

Sign in with Codex or Grok, or set `OPENAI_API_KEY` for the OpenAI provider. Permission modes are `ask` (prompt before a tool runs), `auto`, and `full-access`.

```sh
gx --help
gx <command> --help
```

Inside a session, `/help` lists slash commands.

## License

Apache-2.0. See [LICENSE](LICENSE).
