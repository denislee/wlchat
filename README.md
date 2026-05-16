# wlchat

A native Wayland desktop GUI for chatting with LLMs (Groq, Gemini API, `gemini` CLI).
Written in Go using [Gio](https://gioui.org), which renders directly against
Wayland with no GTK/X11 fallback.

It is the GUI sibling of [lazychat](../lazychat) — same providers, same on-disk
conversation format, just with native windowing instead of a TUI.

## Build

```bash
go build -o wlchat ./cmd/wlchat
```

Requires Go 1.25+, libwayland-client, libwayland-cursor, libwayland-egl, libxkbcommon, and an EGL/Vulkan stack. Most modern Linux desktops ship these.

## Run

```bash
export GROQ_API_KEY="..."     # optional
export GEMINI_API_KEY="..."   # optional
# Or have the `gemini` CLI in PATH.

./wlchat
```

## Keys

| Key            | Action                                  |
|----------------|-----------------------------------------|
| `Ctrl+Enter`   | Send message                            |
| `Ctrl+N`       | New chat                                |
| `Ctrl+M`       | Toggle model picker                     |
| `Ctrl+K`       | Toggle skill picker                     |
| `Esc`          | Close popup / cancel in-flight stream   |
| `Q`            | Quit (when input is not focused)        |

Click the provider pill in the title bar to cycle between configured providers.

## Data

- Linux: `$XDG_DATA_HOME/wlchat` (or `~/.local/share/wlchat`)
- Conversations: `<id>.json`
- Preferences: `config.json`

The `config.json` `skills` array is shared with lazychat — copy yours over and
they'll appear in the skill picker.
