package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/felixfeng33/taskhub/internal/taskhub"
)

func TestCLISharedServer(t *testing.T) {
	dir := t.TempDir()
	store, err := taskhub.OpenStore(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token := "cli-test-token-1234567890"
	h, _ := taskhub.Handler(store, token, "test")
	server := httptest.NewServer(h)
	defer server.Close()
	t.Setenv("TASKHUB_URL", server.URL)
	t.Setenv("TASKHUB_TOKEN", token)
	configPath := filepath.Join(dir, "nonexistent.json")
	call := func(input string, args ...string) (string, error) {
		var out, errOut bytes.Buffer
		args = append(args, "--config", configPath)
		err := run(context.Background(), args, strings.NewReader(input), &out, &errOut)
		return out.String(), err
	}
	body := "# Requirement\n\n你好，remote Mac.\n"
	out, err := call(body, "add", "--title", "Shared task", "--body-file", "-", "--project", " ellie ", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var task taskhub.Task
	json.Unmarshal([]byte(out), &task)
	if task.ID != 1 || task.Body != body || task.Project != "ellie" {
		t.Fatalf("create output: %s", out)
	}
	out, err = call("", "show", "1", "--body-only")
	if err != nil || out != body {
		t.Fatalf("read body: %q %v", out, err)
	}
	_, err = call("", "update", "1", "--status", "in_progress", "--if-status", "pending")
	if err != nil {
		t.Fatal(err)
	}
	_, err = call("", "update", "1", "--status", "in_progress", "--if-status", "pending")
	if e, ok := err.(*apiError); !ok || e.Status != 409 {
		t.Fatalf("claim conflict: %v", err)
	}
	out, err = call("", "list", "--status", "in_progress", "--project", "ellie", "--json")
	if err != nil || !strings.Contains(out, `"id": 1`) {
		t.Fatalf("list: %s %v", out, err)
	}
	out, err = call("", "list", "--project", "plate", "--json")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("filtered list: %s %v", out, err)
	}
	out, err = call("", "update", "1", "--project", "plate", "--json")
	json.Unmarshal([]byte(out), &task)
	if err != nil || task.Project != "plate" || task.Body != body {
		t.Fatalf("project-only update: %s %v", out, err)
	}
	_, err = call("", "update", "1", "--project", "")
	if err != nil {
		t.Fatal(err)
	}
	out, err = call("", "list", "--project", "", "--json")
	if err != nil || !strings.Contains(out, `"id": 1`) {
		t.Fatalf("unassigned filter: %s %v", out, err)
	}
	_, err = call("", "update", "1", "--body", "")
	if err != nil {
		t.Fatal(err)
	}
	out, err = call("", "show", "1", "--body-only")
	if err != nil || out != "" {
		t.Fatalf("clear body: %q %v", out, err)
	}
	_, err = call("", "update", "1")
	if err == nil {
		t.Fatal("empty update accepted")
	}
	out, err = call("", "archive", "1", "--json")
	if err != nil || !strings.Contains(out, `"archived": true`) {
		t.Fatalf("archive: %s %v", out, err)
	}
	out, err = call("", "show", "1", "--json")
	if e, ok := err.(*apiError); !ok || e.Status != 404 || out != "" {
		t.Fatalf("show exposed archive: %s %v", out, err)
	}
	out, err = call("", "list", "--json")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("list exposed archive: %s %v", out, err)
	}
	out, err = call("", "list", "--archived", "--json")
	if err != nil || !strings.Contains(out, `"archived": true`) {
		t.Fatalf("archived list: %s %v", out, err)
	}
	out, err = call("", "show", "1", "--archived", "--json")
	if err != nil || !strings.Contains(out, `"archived": true`) {
		t.Fatalf("explicit archived show: %s %v", out, err)
	}
	_, err = call("", "unarchive", "1")
	if err != nil {
		t.Fatal(err)
	}
	out, err = call("", "show", "1", "--json")
	if err != nil || !strings.Contains(out, `"archived": false`) || !strings.Contains(out, `"status": "in_progress"`) {
		t.Fatalf("restore: %s %v", out, err)
	}
}

func TestProjectFilterRejectsUnfilteredLegacyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"title":"legacy","status":"pending"}]`))
	}))
	defer server.Close()
	t.Setenv("TASKHUB_URL", server.URL)
	t.Setenv("TASKHUB_TOKEN", "test-token-1234567890")
	var out bytes.Buffer
	err := run(context.Background(), []string{"list", "--project", "ellie", "--json", "--config", filepath.Join(t.TempDir(), "config.json")}, strings.NewReader(""), &out, &out)
	if err == nil || !strings.Contains(err.Error(), "upgrade the server") || out.Len() != 0 {
		t.Fatalf("legacy filter silently accepted: %v %s", err, out.String())
	}
}

func TestConfigPrivateAndClientRedirectRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	token := "private-config-token-1234567890"
	var out bytes.Buffer
	err := run(context.Background(), []string{"config", "--config", path, "--url", "https://example.com", "--token-stdin"}, strings.NewReader(token+"\n"), &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), token) {
		t.Fatal("token printed")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions: %v", info.Mode())
	}
	c, err := loadConfig(path)
	if err != nil || c.Token != token {
		t.Fatalf("config read: %+v %v", c, err)
	}
	for _, raw := range []string{"", "ftp://host", "https://user:password@host", "https://host?secret=x"} {
		if _, err := validateURL(raw); err == nil {
			t.Errorf("accepted URL %q", raw)
		}
	}
	var followed atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Store(true)
		w.Write([]byte(`[]`))
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer origin.Close()
	t.Setenv("TASKHUB_URL", origin.URL)
	t.Setenv("TASKHUB_TOKEN", token)
	out.Reset()
	err = run(context.Background(), []string{"list", "--config", path}, strings.NewReader(""), &out, &out)
	if err == nil || !strings.Contains(err.Error(), "redirect refused") || followed.Load() {
		t.Fatalf("redirect policy failed: %v, followed=%v", err, followed.Load())
	}
}
