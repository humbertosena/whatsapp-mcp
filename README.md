# WhatsApp MCP Server

This is a Model Context Protocol (MCP) server for WhatsApp.

With this you can search and read your personal Whatsapp messages (including images, videos, documents, and audio messages), search your contacts and send messages to either individuals or groups. You can also send media files including images, videos, documents, and audio messages.

It connects to your **personal WhatsApp account** directly via the Whatsapp web multidevice API (using the [whatsmeow](https://github.com/tulir/whatsmeow) library). All your messages are stored locally in a SQLite database and only sent to an LLM (such as Claude) when the agent accesses them through tools (which you control).

Here's an example of what you can do when it's connected to Claude.

![WhatsApp MCP](./example-use.png)

> To get updates on this and other projects I work on [enter your email here](https://docs.google.com/forms/d/1rTF9wMBTN0vPfzWuQa2BjfGKdKIpTbyeKxhPMcEzgyI/preview)

> *Caution:* as with many MCP servers, the WhatsApp MCP is subject to [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/). This means that project injection could lead to private data exfiltration.

## Installation

### Prerequisites

- Go
- Python 3.6+
- Anthropic Claude Desktop app (or Cursor)
- UV (Python package manager), install with `curl -LsSf https://astral.sh/uv/install.sh | sh`
- FFmpeg (_optional_) - Only needed for audio messages. If you want to send audio files as playable WhatsApp voice messages, they must be in `.ogg` Opus format. With FFmpeg installed, the MCP server will automatically convert non-Opus audio files. Without FFmpeg, you can still send raw audio files using the `send_file` tool.

### Steps

1. **Clone this repository**

   ```bash
   git clone https://github.com/lharries/whatsapp-mcp.git
   cd whatsapp-mcp
   ```

2. **Run the WhatsApp bridge**

   Navigate to the whatsapp-bridge directory and run the Go application:

   ```bash
   cd whatsapp-bridge
   go run main.go
   ```

   The first time you run it, you will be prompted to scan a QR code. Scan the QR code with your WhatsApp mobile app to authenticate.

   After approximately 20 days, you will might need to re-authenticate.

3. **Connect to the MCP server**

   Copy the below json with the appropriate {{PATH}} values:

   ```json
   {
     "mcpServers": {
       "whatsapp": {
         "command": "{{PATH_TO_UV}}", // Run `which uv` and place the output here
         "args": [
           "--directory",
           "{{PATH_TO_SRC}}/whatsapp-mcp/whatsapp-mcp-server", // cd into the repo, run `pwd` and enter the output here + "/whatsapp-mcp-server"
           "run",
           "main.py"
         ]
       }
     }
   }
   ```

   For **Claude**, save this as `claude_desktop_config.json` in your Claude Desktop configuration directory at:

   ```
   ~/Library/Application Support/Claude/claude_desktop_config.json
   ```

   For **Cursor**, save this as `mcp.json` in your Cursor configuration directory at:

   ```
   ~/.cursor/mcp.json
   ```

4. **Restart Claude Desktop / Cursor**

   Open Claude Desktop and you should now see WhatsApp as an available integration.

   Or restart Cursor.

### Windows Compatibility

If you're running this project on Windows, be aware that `go-sqlite3` requires **CGO to be enabled** in order to compile and work properly. By default, **CGO is disabled on Windows**, so you need to explicitly enable it and have a C compiler installed.

#### Steps to get it working:

1. **Install a C compiler**  
   We recommend using [MSYS2](https://www.msys2.org/) to install a C compiler for Windows. After installing MSYS2, make sure to add the `ucrt64\bin` folder to your `PATH`.  
   → A step-by-step guide is available [here](https://code.visualstudio.com/docs/cpp/config-mingw).

2. **Enable CGO and run the app**

   ```bash
   cd whatsapp-bridge
   go env -w CGO_ENABLED=1
   go run main.go
   ```

Without this setup, you'll likely run into errors like:

> `Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work.`

## Security

The bridge keeps a REST API on your machine and holds the keys to your WhatsApp account, so it is locked down by default:

- The API listens on `127.0.0.1:8080` only, so other machines cannot reach it.
- Every request needs a secret bearer token. Browser requests (CSRF) are rejected.
- Files can only be sent from one dedicated folder.
- Downloaded file names cannot escape `whatsapp-bridge/store/`.
- `store/` is private to your user, and message text is not written to the logs.

### Environment variables

Set these for **both** the bridge and the MCP server when you change them (for the MCP server, use the `env` key of its entry in your MCP client config).

| Variable | Default | What it does |
| --- | --- | --- |
| `WHATSAPP_BRIDGE_ADDR` | `127.0.0.1:8080` | Address the bridge's REST API listens on. The MCP server connects to the same address. Only widen it (for example `0.0.0.0:8080`) if you understand that anything that reaches the port and knows the token can send messages as you. |
| `WHATSAPP_BRIDGE_TOKEN` | generated | Bearer token required by the REST API. Must be at least 16 characters. When unset, a random token is created on first run and stored in `whatsapp-bridge/store/bridge_token`. |
| `WHATSAPP_MEDIA_DIR` | `~/whatsapp-mcp-media` | The only folder `send_file` and `send_audio_message` can read from. Created with mode `0700` if missing. |
| `WHATSAPP_BRIDGE_LOG_CONTENT` | unset | Set to `1` to log message text, sender, file names, recipients and paths while debugging. Without it the bridge logs only direction, media type and size. |

### API token

- By default the token is a random 32-byte hex string saved in `whatsapp-bridge/store/bridge_token` (mode `0600`) the first time the bridge starts. Later runs reuse it.
- The MCP server reads the token from `WHATSAPP_BRIDGE_TOKEN`, or else from that file. You do not need to copy it anywhere. Start the bridge once before using the MCP server.
- If you set `WHATSAPP_BRIDGE_TOKEN` yourself, give the same value to the bridge and to the MCP server.
- To rotate the token, delete `whatsapp-bridge/store/bridge_token` and restart the bridge.
- Calling the API by hand needs the token, a JSON content type, and no `Origin` header:

  ```bash
  curl -X POST http://127.0.0.1:8080/api/send \
    -H "Authorization: Bearer $(cat whatsapp-bridge/store/bridge_token)" \
    -H "Content-Type: application/json" \
    -d '{"recipient": "123456789", "message": "hello"}'
  ```

### Sending files

`send_file` and `send_audio_message` only accept files inside `WHATSAPP_MEDIA_DIR`. Copy the file there first. Symlinks are resolved before the check, so a link that points outside the folder is rejected, and so are directories. Audio that needs converting to Opus is converted in `WHATSAPP_MEDIA_DIR/.tmp/` and deleted right after sending.

### Ask for human approval before sending

Anything that reads your messages can be tricked by text inside those messages (prompt injection) into sending messages or files for you. Configure your MCP client to **ask you before running** these tools instead of auto-approving them:

- `send_message`
- `send_file`
- `send_audio_message`

For example, in Claude Code put them under `ask` in `permissions`: `mcp__whatsapp__send_message`, `mcp__whatsapp__send_file` and `mcp__whatsapp__send_audio_message` (the server is named `whatsapp` in the config above). In Claude Desktop, do not choose "Always allow" for them. In Cursor, keep auto-run off for MCP tools.

### Protect your session data

`whatsapp-bridge/store/whatsapp.db` contains your WhatsApp **session keys**. Whoever has a copy can use your WhatsApp account without scanning a QR code. Do not copy, share, commit, upload or back it up unencrypted. `messages.db` holds your whole message history and `bridge_token` unlocks the API, so treat them the same way. The bridge makes everything in `store/` readable only by your user, and `.gitignore` excludes the folder. If a copy of `whatsapp.db` ever leaks, remove the linked device in WhatsApp (Settings > Linked Devices) and delete the file.

## Architecture Overview

This application consists of two main components:

1. **Go WhatsApp Bridge** (`whatsapp-bridge/`): A Go application that connects to WhatsApp's web API, handles authentication via QR code, and stores message history in SQLite. It serves as the bridge between WhatsApp and the MCP server.

2. **Python MCP Server** (`whatsapp-mcp-server/`): A Python server implementing the Model Context Protocol (MCP), which provides standardized tools for Claude to interact with WhatsApp data and send/receive messages.

### Data Storage

- All message history is stored in a SQLite database within the `whatsapp-bridge/store/` directory
- The database maintains tables for chats and messages
- Messages are indexed for efficient searching and retrieval

## Usage

Once connected, you can interact with your WhatsApp contacts through Claude, leveraging Claude's AI capabilities in your WhatsApp conversations.

### MCP Tools

Claude can access the following tools to interact with WhatsApp:

- **search_contacts**: Search for contacts by name or phone number
- **list_messages**: Retrieve messages with optional filters and context
- **list_chats**: List available chats with metadata
- **get_chat**: Get information about a specific chat
- **get_direct_chat_by_contact**: Find a direct chat with a specific contact
- **get_contact_chats**: List all chats involving a specific contact
- **get_last_interaction**: Get the most recent message with a contact
- **get_message_context**: Retrieve context around a specific message
- **send_message**: Send a WhatsApp message to a specified phone number or group JID
- **send_file**: Send a file (image, video, raw audio, document) to a specified recipient. The file must be inside `WHATSAPP_MEDIA_DIR` (see [Security](#security))
- **send_audio_message**: Send an audio file as a WhatsApp voice message (requires the file to be an .ogg opus file or ffmpeg must be installed). The file must be inside `WHATSAPP_MEDIA_DIR`
- **download_media**: Download media from a WhatsApp message and get the local file path

### Media Handling Features

The MCP server supports both sending and receiving various media types:

#### Media Sending

You can send various media types to your WhatsApp contacts:

- **Images, Videos, Documents**: Use the `send_file` tool to share any supported media type. Files must be inside `WHATSAPP_MEDIA_DIR` (default `~/whatsapp-mcp-media`).
- **Voice Messages**: Use the `send_audio_message` tool to send audio files as playable WhatsApp voice messages.
  - For optimal compatibility, audio files should be in `.ogg` Opus format.
  - With FFmpeg installed, the system will automatically convert other audio formats (MP3, WAV, etc.) to the required format.
  - Without FFmpeg, you can still send raw audio files using the `send_file` tool, but they won't appear as playable voice messages.

#### Media Downloading

By default, just the metadata of the media is stored in the local database. The message will indicate that media was sent. To access this media you need to use the download_media tool which takes the `message_id` and `chat_jid` (which are shown when printing messages containing the meda), this downloads the media and then returns the file path which can be then opened or passed to another tool.

## Technical Details

1. Claude sends requests to the Python MCP server
2. The MCP server queries the Go bridge for WhatsApp data or directly to the SQLite database
3. The Go accesses the WhatsApp API and keeps the SQLite database up to date
4. Data flows back through the chain to Claude
5. When sending messages, the request flows from Claude through the MCP server to the Go bridge and to WhatsApp

## Troubleshooting

- If you encounter permission issues when running uv, you may need to add it to your PATH or use the full path to the executable.
- Make sure both the Go application and the Python server are running for the integration to work properly.
- **"WhatsApp bridge token not found"**: start the bridge once so it creates `whatsapp-bridge/store/bridge_token`, or set `WHATSAPP_BRIDGE_TOKEN` for the MCP server.
- **HTTP 401 from the bridge**: the MCP server and the bridge use different tokens. Remove `WHATSAPP_BRIDGE_TOKEN` from one of them or give both the same value.
- **"Media files must be inside the allowed media directory"**: copy the file into `WHATSAPP_MEDIA_DIR` (default `~/whatsapp-mcp-media`) and use that path.

### Authentication Issues

- **QR Code Not Displaying**: If the QR code doesn't appear, try restarting the authentication script. If issues persist, check if your terminal supports displaying QR codes.
- **WhatsApp Already Logged In**: If your session is already active, the Go bridge will automatically reconnect without showing a QR code.
- **Device Limit Reached**: WhatsApp limits the number of linked devices. If you reach this limit, you'll need to remove an existing device from WhatsApp on your phone (Settings > Linked Devices).
- **No Messages Loading**: After initial authentication, it can take several minutes for your message history to load, especially if you have many chats.
- **WhatsApp Out of Sync**: If your WhatsApp messages get out of sync with the bridge, delete both database files (`whatsapp-bridge/store/messages.db` and `whatsapp-bridge/store/whatsapp.db`) and restart the bridge to re-authenticate.

For additional Claude Desktop integration troubleshooting, see the [MCP documentation](https://modelcontextprotocol.io/quickstart/server#claude-for-desktop-integration-issues). The documentation includes helpful tips for checking logs and resolving common issues.
