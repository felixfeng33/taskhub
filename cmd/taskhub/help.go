package main

import (
	"flag"
	"fmt"
)

const help = `taskhub - shared tasks and Markdown documents for people and coding agents

COMMANDS
  taskhub add --title TITLE [--body TEXT | --body-file PATH] [--project NAME] [--status STATUS]
  taskhub list [--project NAME] [--status STATUS] [--archived] [--limit 100] [--after ID]
  taskhub show ID [--body-only] [--archived]
  taskhub update ID [--title TITLE] [--body TEXT | --body-file PATH]
                   [--project NAME] [--status STATUS] [--if-status STATUS]
  taskhub archive ID
  taskhub unarchive ID
  taskhub config --url URL --token-stdin
  taskhub serve [--listen 127.0.0.1:8080] [--db PATH] [--token-file PATH]
  taskhub skill                         Print the bundled AI skill manual
  taskhub skill install [--dir PATH]    Install $taskhub for Codex on this machine
  taskhub version
  taskhub help COMMAND                  Detailed usage, examples, and flags

QUICK START
  taskhub config --url https://tasks.example.com --token-stdin < token.txt
  taskhub add --title 'Implement sign-in' --project ellie --body-file spec.md --json
  taskhub list --project ellie --status pending --json
  taskhub show 12 --json
  taskhub update 12 --status in_progress --if-status pending --json
  taskhub update 12 --status review --json
  taskhub update 12 --status done --json

NATURAL LANGUAGE WITH AI
  Install once on each Codex client: taskhub skill install
  Then ask Codex, for example:
    $taskhub 把刚才的定稿保存成 ellie 的待执行任务。
    $taskhub 给任务 12 追加验收标准，保留其他正文。
    $taskhub 归档 plate 已完成的任务。
  View the full operating guide offline with: taskhub skill
  The CLI remains a command tool; the AI interprets your natural-language request.

TASK FIELDS
  id        Server-generated positive integer. Put it immediately after the command.
  title     Single line, 1-200 Unicode characters.
  body      Markdown, up to 1 MiB. Use --body-file - to read stdin.
  project   Optional, case-sensitive name, up to 80 characters; outer whitespace trimmed.
  status    pending (待执行), in_progress (执行中), review (待验收), done (已完成).
  archived  Separate flag; archiving preserves the original fields and status.

EDITING DOCUMENTS
  Omitting a field preserves it. Updating body replaces the ENTIRE document.
  Read the latest body, edit a UTF-8 file, then upload the complete edited document:
    taskhub show 12 --body-only > task-12.md
    taskhub update 12 --body-file task-12.md --json
  Preserve unrelated sections when appending feedback. There is no body version lock.
  Use --body '' only to clear a body, and --project '' to remove a project.

FINDING TASKS
  Prefer --json for agents and scripts. A list contains summaries, not full bodies.
  Default page size is 100, maximum 1000; tasks are ordered by ascending ID.
  To list all matches, pass the last returned ID as --after until a page is empty.
  No --project means all projects; list --project '' means tasks without a project.
  Resolve ambiguous titles to IDs before editing. Project names do not select a Git repo.

ARCHIVE AND RESTORE
  taskhub archive 12                    Hide a task without deleting it
  taskhub list --archived               List only archived tasks
  taskhub show 12 --archived --json      Explicitly read a hidden task
  taskhub unarchive 12                  Restore its original content and status
  Default list/show hide archived tasks. Restore a task before editing its fields.

CONFLICTS AND ERRORS
  --if-status pending makes claiming a pending task atomic; it is not a body lock.
  Exit 0: success. Exit 3 / HTTP 409: status precondition failed; re-read the task.
  Exit 1: other error. HTTP 404 may mean the task is archived; use explicit archived view.
  HTTP 401: check existing authentication. Connection refused: check the URL or tunnel.
  A timed-out write may have succeeded; verify before retrying a create operation.

CONNECTIONS
  Client commands accept --url URL, --config PATH, and --json.
  URL precedence: --url > TASKHUB_URL > saved config. TASKHUB_TOKEN overrides saved token.
  Client config: OS user config directory / taskhub/config.json, file mode 0600.
  Keep tokens out of task documents and logs. Use the existing connection for task edits.
  serve requires TASKHUB_TOKEN or --token-file (at least 16 bytes); it serves HTTP.
  Use HTTPS or an SSH tunnel for remote access. The server stores tasks in SQLite.

Docs: https://github.com/felixfeng33/taskhub
`

var commandHelp = map[string]string{
	"add": `Usage: taskhub add --title TITLE [options]
Create a task. Defaults: status=pending, empty body, no project.
  taskhub add --title 'Fix editor' --project plate --body-file spec.md --json
  taskhub add --title 'Draft requirement' --body-file - --json
Use the returned ID for later changes. Do not blindly retry a timed-out create.`,
	"list": `Usage: taskhub list [options]
List active task summaries. Use show for the full Markdown body.
  taskhub list --project ellie --status pending --json
  taskhub list --project '' --json
  taskhub list --archived --project plate --json
  taskhub list --limit 1000 --after 1000 --json
For complete results, keep paging with the last returned ID until the array is empty.`,
	"show": `Usage: taskhub show ID [options]
Read one task. Archived tasks return 404 unless --archived is specified.
  taskhub show 12 --json
  taskhub show 12 --body-only > task-12.md
  taskhub show 12 --archived --json
--body-only preserves the exact body, including its trailing newline.
--body-only and --json are mutually exclusive.`,
	"update": `Usage: taskhub update ID [options]
Change only supplied fields. Body updates replace the entire document.
  taskhub update 12 --title 'Revised title' --project ellie --json
  taskhub update 12 --body-file revised-spec.md --json
  taskhub update 12 --status in_progress --if-status pending --json
  taskhub update 12 --status review --json
  taskhub update 12 --project '' --json
Read the current body before adding feedback and preserve unrelated sections.
An explicit --body '' clears the body. Restore archived tasks before editing.
Exit 3 means --if-status did not match; re-read rather than removing the precondition.`,
	"archive": `Usage: taskhub archive ID [options]
Hide a task from default list/show while retaining its content, project, and status.
  taskhub archive 12 --json
  taskhub show 12 --archived --json
This does not mark it done or delete it. Repeated archive calls are safe.`,
	"unarchive": `Usage: taskhub unarchive ID [options]
Restore an archived task with its original content, project, and status.
  taskhub unarchive 12 --json
  taskhub show 12 --json
Repeated unarchive calls are safe.`,
	"config": `Usage: taskhub config --url URL --token-stdin [options]
Save client connection settings in a private file. Existing omitted values are kept.
  taskhub config --url https://tasks.example.com --token-stdin < token.txt
  taskhub config --url http://127.0.0.1:18080
The second example assumes a configured SSH tunnel and an existing saved token.
Tokens are read from stdin, never printed, and are not accepted as CLI arguments.`,
	"serve": `Usage: taskhub serve [options]
Run the HTTP server with SQLite persistence. This is needed only on the server.
  taskhub serve --listen 127.0.0.1:8080 --db tasks.db --token-file token.txt
Use TASKHUB_TOKEN instead of --token-file if preferred. A token needs at least 16 bytes.
Schema upgrades run on startup and preserve tasks. Back up existing databases first.
Task endpoints require a Bearer token; /healthz returns public health/version information.
Use HTTPS through a reverse proxy or an SSH tunnel for remote access.`,
}

func setUsage(f *flag.FlagSet, description string) {
	f.Usage = func() {
		fmt.Fprintln(f.Output(), description)
		fmt.Fprintln(f.Output(), "\nOptions:")
		f.PrintDefaults()
	}
}
