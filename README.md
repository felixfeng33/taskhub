# taskhub

A shared task list for your laptop, remote Mac, and server. One binary runs the HTTP server or acts as its CLI client. Tasks contain a title, a Markdown body, a status, an optional project name, and an automatically assigned ID.

[中文说明](README.zh-CN.md) · [Releases](https://github.com/felixfeng33/taskhub/releases)

## Install

Download the macOS or Linux archive matching your machine from Releases, verify it against `checksums.txt`, and put `taskhub` on your PATH. Both Apple Silicon/ARM64 and Intel/AMD64 builds are available. Go, Node.js, and a separate SQLite installation are not needed to run the binary.

Or download and inspect the installer, then run it:

```sh
curl -fsSL https://raw.githubusercontent.com/felixfeng33/taskhub/main/scripts/install.sh -o install-taskhub.sh
sh install-taskhub.sh v0.3.0
export PATH="$HOME/.local/bin:$PATH"
taskhub version
```

The installer verifies the archive's SHA-256 checksum. Set `INSTALL_DIR` to choose a different destination. Run it again with a new version to upgrade; client configuration and the server database are stored separately.

From source, with the Go version specified in `go.mod` or newer:

```sh
go install github.com/felixfeng33/taskhub/cmd/taskhub@latest
```

## Use with Codex and natural language

Install the bundled skill on each machine running Codex:

```sh
taskhub skill install
```

The destination is `$CODEX_HOME/skills/taskhub`, or `~/.codex/skills/taskhub` when `CODEX_HOME` is unset. If Codex has not refreshed skill discovery, start a new task. The skill uses that machine's existing taskhub client configuration.

Then write prompts in Codex such as:

```text
$taskhub Save our final requirements as a pending task in the ellie project.
$taskhub Add these acceptance criteria to task 12 and preserve the rest of its body.
$taskhub Archive the completed tasks in plate.
```

The CLI remains deterministic; Codex interprets natural language and calls it. The [skill manual](skills/taskhub/SKILL.md) explains task lookup, pagination, status mapping, whole-body edits, archive/restore, conflict handling, and read-back verification.

The binary contains the same manual and UI metadata as the repository, so no separate clone or download is required:

```sh
taskhub --help                    # operating manual and examples
taskhub help update               # command-specific examples and flags
taskhub update --help             # equivalent command help
taskhub skill                     # print the full SKILL.md offline
taskhub skill install --dir /path/to/skills/taskhub
```

Installing an identical skill is a no-op. If the managed files differ, installation stops before writing anything. Review local edits before using `taskhub skill install --force`; it replaces `SKILL.md` and `agents/openai.yaml` while preserving other files. Symlink destinations are refused. Installing a skill does not alter server settings or task data.

These help and skill commands are available in v0.3.0 and do not require server access. The task operations described by the skill work with v0.2.0 and newer servers. After upgrading a CLI, rerun the skill installer to check for manual updates.

## Start the server

Generate one shared token and keep it on your three devices:

```sh
mkdir -p taskhub-data
chmod 700 taskhub-data
umask 077
openssl rand -hex 32 > taskhub-data/token
taskhub serve --db taskhub-data/tasks.db --token-file taskhub-data/token
```

The default listener is `127.0.0.1:8080`. Use HTTPS through a reverse proxy for internet access, or reach the loopback listener through an SSH tunnel. For a trusted private network, choose the appropriate bind address with `--listen`. Taskhub itself serves HTTP, not TLS.

`TASKHUB_TOKEN` can replace `--token-file`. Tokens must contain at least 16 bytes. All task endpoints require the token; `GET /healthz` is public and returns only service status and version.

## Configure each Mac

```sh
# Use your server's HTTPS URL. For a same-machine test, use http://127.0.0.1:8080.
taskhub config --url https://tasks.example.com --token-stdin < taskhub-data/token
```

`config` saves a private, mode-0600 JSON file under your OS user configuration directory: `~/Library/Application Support/taskhub/config.json` on macOS, or `$XDG_CONFIG_HOME/taskhub/config.json` / `~/.config/taskhub/config.json` on Linux. Client commands accept `--config PATH`.

Overrides: `--url` takes precedence over `TASKHUB_URL`, which takes precedence over the saved URL. `TASKHUB_TOKEN` overrides the saved token. Tokens are not printed or accepted as command-line arguments.

## Commands

```sh
taskhub add --title "Implement sign-in" --project ellie --body-file spec.md
taskhub list
taskhub list --project ellie --status pending --json
taskhub show 1
taskhub show 1 --body-only > spec.md
taskhub update 1 --title "Implement email sign-in"
taskhub update 1 --project ellie
taskhub update 1 --body-file revised-spec.md
taskhub update 1 --status in_progress --if-status pending
taskhub update 1 --status review
taskhub update 1 --status done
```

`add`, `list`, `show`, `update`, `archive`, and `unarchive` accept `--json`. Place the ID immediately after `show`, `update`, `archive`, or `unarchive`, then pass flags. Use `--body-file -` to read stdin. An explicit `--body ''` clears the body; omitted fields remain unchanged. `show --body-only` emits the original body without changing its trailing newline. Human-readable output strips terminal control characters; JSON and `--body-only` preserve the stored content.

Statuses are `pending`, `in_progress`, `review`, and `done`. New tasks default to `pending`. Status transitions are flexible, so rejected work can return to `pending`.

To avoid two agents taking the same pending task, both should run:

```sh
taskhub update 1 --status in_progress --if-status pending --json
```

The check and update happen atomically. Only one client succeeds; the other receives HTTP 409 and CLI exit code 3. Other failures exit with code 1. Success exits with code 0. This is a status precondition, not a worker lease: interrupted work remains `in_progress` until you update it.

Taskhub does not start agents, run Git commands, or infer completion. Your agent reads the body and explicitly updates the status. Put implementation instructions, acceptance criteria, feedback, and commit IDs in the body. Body updates replace the whole body; concurrent unconditional writes use the last accepted write. Keep one editor per task when editing requirements.

## Projects

A task belongs to zero or one project. Project names are plain, case-sensitive strings; use a name directly without registering it first. Leading and trailing whitespace is trimmed. Names can contain up to 80 Unicode characters and cannot contain control characters.

```sh
taskhub add --title "Fix editor selection" --project plate --body-file spec.md
taskhub list --project plate --status pending
taskhub update 1 --project ellie
taskhub update 1 --project ''     # remove the project
taskhub list --project ''         # only tasks without a project
taskhub list                     # all projects
```

JSON uses `"project":""` for tasks without a project. Omitting `project` in an update keeps its current value. Projects filter tasks; they do not automatically select a Git checkout or start an agent.

## Archive and restore

```sh
taskhub archive 1
taskhub list                          # archived tasks are hidden
taskhub show 1                        # archived tasks return "task not found"
taskhub list --archived --project ellie
taskhub show 1 --archived              # explicitly inspect an archived task
taskhub unarchive 1
```

Archiving preserves the title, body, project, and workflow status. `archived` is a separate boolean, not a workflow status. Default `list` and `show` hide archived tasks on the server, including for older clients. `list --archived` returns only archived tasks; `show --archived` allows reading a task even if it is archived. Restore a task before editing its fields. Repeated archive or unarchive calls are safe.

### Upgrading from v0.1.0

Upgrade the server first, then the clients. On startup, the server adds the `project` and `archived` columns in a transaction if they are missing. Existing task IDs, titles, bodies, and statuses are preserved, and existing tasks have an empty project and are not archived. Repeated startup is safe. Back up the database before updating an existing deployment.

The v0.1.0 client continues to read and update tasks through the new server; its updates preserve projects. The project and archive commands require a v0.2.0 or newer client and server.

## HTTP API

Send `Authorization: Bearer <token>` on every task request and `Content-Type: application/json` on writes.

| Method | Path | Result |
| --- | --- | --- |
| POST | `/tasks` | Create a task, HTTP 201 |
| GET | `/tasks` | List summaries containing `id`, `title`, `status`, `project`, `archived` |
| GET | `/tasks/{id}` | Read an active task; add `?archived=true` to allow archived tasks |
| PATCH | `/tasks/{id}` | Update selected fields, or archive/restore |
| GET | `/healthz` | Public health check |

Create body:

```json
{"title":"Implement sign-in","body":"# Requirements\n...","status":"pending","project":"ellie"}
```

Update body:

```json
{"status":"in_progress","if_status":"pending"}
```

Archive and restore use separate PATCH requests:

```json
{"archived":true}
```

```json
{"archived":false}
```

Do not combine `archived` with edits to the title, body, project, or status. An optional `if_status` precondition is accepted. Editing archived tasks returns 404 until they are restored.

Task response:

```json
{"id":1,"title":"Implement sign-in","body":"# Requirements\n...","status":"pending","project":"ellie","archived":false}
```

List query parameters: `archived` (`false` by default, `true` for archived tasks only), `status`, `project` (exact match; an explicit empty value selects unassigned tasks), `limit` (default 100, maximum 1000), and `after` (exclusive ID). Results use ascending ID order. To continue listing, use the last returned ID as `after` until an empty array is returned. Titles are single-line strings of 1–200 Unicode characters; bodies can contain up to 1 MiB of UTF-8 text.

API errors contain `{"error":"message"}` for validation, authentication, missing tasks, and conflicts. Unknown routes and unsupported methods use Go's standard HTTP responses. There is one shared workspace and one token, with no per-user permissions or document history.

## Linux service

Copy the binary to `/usr/local/bin/taskhub`. Create a `taskhub` system user and group, place the token in `/etc/taskhub/token` readable by that user, and install [deploy/taskhub.service](deploy/taskhub.service) into your systemd system unit directory. Then enable and start it:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now taskhub
```

The service stores data in `/var/lib/taskhub`. The [Caddy example](deploy/Caddyfile.example) shows an HTTPS reverse proxy; replace its hostname with your domain.

Back up with SQLite's online backup API or stop the service before copying the database. Do not copy only the `.db` file from a running WAL database. Upgrading the binary leaves task data in place. Schema initialization and the project and archive migrations run automatically on startup; there is no separate migration command.

## Development and releases

```sh
go test -race ./...
go vet ./...
go build -o bin/taskhub ./cmd/taskhub
sh scripts/build-release.sh v0.3.0
```

CI tests macOS and Linux. Pushing a `v*` tag runs tests, builds four platform archives, and publishes a GitHub Release with checksums. Release binaries bundle dependency license notices.

## License

MIT. See [LICENSE](LICENSE) and [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES).
