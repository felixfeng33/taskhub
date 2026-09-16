package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/felixfeng33/taskhub/internal/taskhub"
)

var version = "dev"

const help = `taskhub - shared tasks for people and coding agents

Usage:
  taskhub serve [--listen 127.0.0.1:8080] [--db PATH] [--token-file PATH]
  taskhub config --url URL --token-stdin
  taskhub add --title TITLE [--body TEXT | --body-file PATH] [--status STATUS]
  taskhub list [--status STATUS] [--limit 100] [--after ID]
  taskhub show ID [--body-only]
  taskhub update ID [--title TITLE] [--body TEXT | --body-file PATH] [--status STATUS]
                   [--if-status STATUS]
  taskhub version

Client commands accept --url URL, --config PATH, and --json.
Configuration: flags > TASKHUB_URL / TASKHUB_TOKEN > local config.
Use --body-file - to read Markdown from stdin.
Statuses: pending, in_progress, review, done.
Run a command with --help for its options.
`

type config struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "taskhub:", safeText(err.Error()))
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Status == 409 {
			os.Exit(3)
		}
		os.Exit(1)
	}
}

func defaultConfig() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "taskhub", "config.json"), nil
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(stdout, help)
		return err
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintln(stdout, version)
		return nil
	}
	command, rest := args[0], args[1:]
	if command == "serve" {
		return serve(ctx, rest, stderr)
	}
	if command != "config" && command != "add" && command != "list" && command != "show" && command != "update" {
		return fmt.Errorf("unknown command %q; use taskhub --help", command)
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(stderr)
	path, err := defaultConfig()
	if err != nil {
		return err
	}
	configPath := f.String("config", path, "client config file")
	serverURL := f.String("url", "", "server URL")
	jsonOutput := f.Bool("json", false, "print machine-readable JSON")
	var title, body, bodyFile, status, ifStatus string
	var bodyOnly, tokenStdin bool
	var limit int
	var after int64
	var id string
	if command == "show" || command == "update" {
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			id, rest = rest[0], rest[1:]
		}
	}
	switch command {
	case "config":
		f.BoolVar(&tokenStdin, "token-stdin", false, "read and save token from stdin")
	case "add", "update":
		f.StringVar(&title, "title", "", "task title")
		f.StringVar(&body, "body", "", "Markdown body")
		f.StringVar(&bodyFile, "body-file", "", "Markdown file, or - for stdin")
		f.StringVar(&status, "status", "", "pending, in_progress, review, done")
		if command == "update" {
			f.StringVar(&ifStatus, "if-status", "", "update only if current status matches")
		}
	case "list":
		f.StringVar(&status, "status", "", "filter by status")
		f.IntVar(&limit, "limit", 100, "maximum results, 1 to 1000")
		f.Int64Var(&after, "after", 0, "return tasks after this ID")
	case "show":
		f.BoolVar(&bodyOnly, "body-only", false, "print exact Markdown body")
	}
	if err := f.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments; put the task ID immediately after show or update")
	}
	seen := map[string]bool{}
	f.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	c, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if command == "config" {
		if !seen["url"] && !tokenStdin {
			return errors.New("provide --url and/or --token-stdin")
		}
		if seen["url"] {
			c.URL = *serverURL
		}
		if tokenStdin {
			b, err := io.ReadAll(io.LimitReader(stdin, 4097))
			if err != nil {
				return err
			}
			if len(b) > 4096 {
				return errors.New("token is too long")
			}
			c.Token = strings.TrimSpace(string(b))
		}
		if _, err := validateURL(c.URL); err != nil {
			return err
		}
		if len(c.Token) < 16 {
			return errors.New("token must contain at least 16 bytes; use --token-stdin")
		}
		if err := saveConfig(*configPath, c); err != nil {
			return err
		}
		if *jsonOutput {
			return printJSON(stdout, map[string]string{"config": *configPath, "url": c.URL})
		}
		fmt.Fprintf(stdout, "Saved configuration to %s\n", *configPath)
		return nil
	}
	if v := os.Getenv("TASKHUB_URL"); v != "" {
		c.URL = v
	}
	if v := os.Getenv("TASKHUB_TOKEN"); v != "" {
		c.Token = v
	}
	if seen["url"] {
		c.URL = *serverURL
	}
	base, err := validateURL(c.URL)
	if err != nil {
		return err
	}
	if c.Token == "" {
		return errors.New("set TASKHUB_TOKEN or run taskhub config --url URL --token-stdin")
	}
	if command == "show" || command == "update" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n < 1 {
			return errors.New("provide a positive task ID immediately after the command")
		}
	}
	if status != "" && !taskhub.ValidStatus(status) {
		return errors.New("invalid status")
	}
	if seen["status"] && status == "" {
		return errors.New("status cannot be empty")
	}
	if seen["if-status"] && !taskhub.ValidStatus(ifStatus) {
		return errors.New("invalid if-status")
	}
	if seen["body"] && seen["body-file"] {
		return errors.New("use either --body or --body-file")
	}
	if seen["body-file"] {
		reader := stdin
		if bodyFile != "-" {
			file, err := os.Open(bodyFile)
			if err != nil {
				return err
			}
			defer file.Close()
			reader = file
		}
		b, err := io.ReadAll(io.LimitReader(reader, taskhub.MaxBodyBytes+1))
		if err != nil {
			return err
		}
		if len(b) > taskhub.MaxBodyBytes {
			return errors.New("body must not exceed 1 MiB")
		}
		body = string(b)
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("redirect refused; configure the final server URL")
	}}
	if command == "list" {
		if limit < 1 || limit > 1000 || after < 0 {
			return errors.New("limit must be 1..1000 and after must be nonnegative")
		}
		query := url.Values{"limit": {strconv.Itoa(limit)}, "after": {strconv.FormatInt(after, 10)}}
		if status != "" {
			query.Set("status", status)
		}
		var tasks []taskhub.Summary
		if err := request(ctx, client, c, "GET", base+"/tasks?"+query.Encode(), nil, &tasks); err != nil {
			return err
		}
		if *jsonOutput {
			return printJSON(stdout, tasks)
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATUS\tTITLE")
		for _, t := range tasks {
			fmt.Fprintf(w, "%d\t%s\t%s\n", t.ID, t.Status, safeText(t.Title))
		}
		return w.Flush()
	}
	var t taskhub.Task
	method, endpoint := "GET", base+"/tasks/"+id
	var payload any
	if command == "add" {
		if status == "" {
			status = "pending"
		}
		if err := taskhub.Validate(strings.TrimSpace(title), body, status); err != nil {
			return err
		}
		method, endpoint = "POST", base+"/tasks"
		payload = map[string]string{"title": title, "body": body, "status": status}
	} else if command == "update" {
		u := taskhub.Update{}
		if seen["title"] {
			u.Title = &title
		}
		if seen["body"] || seen["body-file"] {
			u.Body = &body
		}
		if seen["status"] {
			u.Status = &status
		}
		if seen["if-status"] {
			u.IfStatus = &ifStatus
		}
		if u.Title == nil && u.Body == nil && u.Status == nil {
			return errors.New("provide --title, --body, --body-file, or --status")
		}
		method, payload = "PATCH", u
	}
	if err := request(ctx, client, c, method, endpoint, payload, &t); err != nil {
		return err
	}
	if bodyOnly && *jsonOutput {
		return errors.New("use either --body-only or --json")
	}
	if bodyOnly {
		_, err := io.WriteString(stdout, t.Body)
		return err
	}
	if *jsonOutput {
		return printJSON(stdout, t)
	}
	fmt.Fprintf(stdout, "#%d [%s] %s\n", t.ID, t.Status, safeText(t.Title))
	if command == "show" && t.Body != "" {
		fmt.Fprintf(stdout, "\n%s\n", safeText(t.Body))
	}
	return nil
}

func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' || r >= 127 && r <= 159 {
			return -1
		}
		return r
	}, s)
}

func printJSON(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

func validateURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("set a valid http(s) server URL using --url, TASKHUB_URL, or taskhub config")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func loadConfig(path string) (config, error) {
	var c config
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return c, nil
}

func saveConfig(path string, c config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".taskhub-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(c); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("server returned %d: %s", e.Status, e.Message) }

func request(ctx context.Context, client *http.Client, c config, method, endpoint string, payload, output any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer res.Body.Close()
	reader := io.LimitReader(res.Body, 8*taskhub.MaxBodyBytes)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(reader).Decode(&problem) != nil || problem.Error == "" {
			problem.Error = http.StatusText(res.StatusCode)
		}
		return &apiError{Status: res.StatusCode, Message: problem.Error}
	}
	if err := json.NewDecoder(reader).Decode(output); err != nil {
		return fmt.Errorf("invalid server response: %w", err)
	}
	return nil
}

func serve(ctx context.Context, args []string, stderr io.Writer) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	f.SetOutput(stderr)
	listen := f.String("listen", "127.0.0.1:8080", "HTTP listening address")
	path, err := defaultConfig()
	if err != nil {
		return err
	}
	dbPath := f.String("db", filepath.Join(filepath.Dir(path), "taskhub.db"), "SQLite database file")
	tokenFile := f.String("token-file", "", "read server token from a file instead of TASKHUB_TOKEN")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected serve arguments")
	}
	token := os.Getenv("TASKHUB_TOKEN")
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(b))
	}
	if len(token) < 16 || strings.TrimSpace(token) != token {
		return errors.New("set TASKHUB_TOKEN or --token-file to a token of at least 16 bytes")
	}
	store, err := taskhub.OpenStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	handler, err := taskhub.Handler(store, token, version)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	fmt.Fprintf(stderr, "taskhub %s listening on %s\n", version, listener.Addr())
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			server.Close()
			return err
		}
		return nil
	}
}
