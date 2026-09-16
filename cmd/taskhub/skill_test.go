package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	bundled "github.com/felixfeng33/taskhub/skills"
)

func TestSkillInstallOfflineAndPreserveLocalEdits(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	t.Setenv("TASKHUB_URL", "not-a-server-url")
	var out bytes.Buffer
	runCLI := func(args ...string) error {
		out.Reset()
		return run(context.Background(), args, strings.NewReader(""), &out, &out)
	}
	if err := runCLI("skill"); err != nil {
		t.Fatal(err)
	}
	manual, err := bundled.Files.ReadFile("taskhub/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), manual) {
		t.Fatal("skill output differs from its bundled manual")
	}
	if err := runCLI("skill", "install"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "skills", "taskhub")
	for _, name := range []string{"SKILL.md", "agents/openai.yaml"} {
		want, _ := bundled.Files.ReadFile("taskhub/" + name)
		got, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("incomplete install %s: %v", name, err)
		}
	}
	metadata := filepath.Join(destination, "agents", "openai.yaml")
	if err := os.WriteFile(metadata, []byte("custom local metadata\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// If one managed file differs, do not partially replace another missing file.
	if err := os.Remove(filepath.Join(destination, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := runCLI("skill", "install"); err == nil {
		t.Fatal("overwrote a modified installation without --force")
	}
	if _, err := os.Stat(filepath.Join(destination, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("wrote part of the skill before detecting the conflict")
	}
	custom := filepath.Join(destination, "local-notes.txt")
	if err := os.WriteFile(custom, []byte("keep me"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runCLI("skill", "install", "--force"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(custom); err != nil || string(b) != "keep me" {
		t.Fatal("unrelated skill file was modified")
	}
	info, err := os.Stat(filepath.Join(destination, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runCLI("skill", "install"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(destination, "SKILL.md"))
	if !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("identical reinstall rewrote the manual")
	}
}

func TestSkillInstallCustomDirectoryAndSymlinkRefusal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "unused-default"))
	var out bytes.Buffer
	destination := filepath.Join(root, "my-skill")
	if err := runSkill([]string{"install", "--dir", destination}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "unused-default")); !os.IsNotExist(err) {
		t.Fatal("custom install wrote to default home")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(destination, link); err != nil {
		t.Fatal(err)
	}
	if err := runSkill([]string{"install", "--dir", link, "--force"}, &out, &out); err == nil {
		t.Fatal("followed a skill directory symlink")
	}
}

func TestHelpRequiresNoConfigOrServerAndDoesNotInstall(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	root := t.TempDir()
	t.Setenv("TASKHUB_URL", server.URL)
	t.Setenv("TASKHUB_TOKEN", "")
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	badConfig := filepath.Join(root, "config.json")
	if err := os.WriteFile(badConfig, []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := [][]string{{"--help"}, {"help"}, {"help", "version"}, {"help", "serve"}, {"help", "skill"}, {"skill", "install", "--help"}}
	for _, command := range []string{"add", "list", "show", "update", "archive", "unarchive", "config"} {
		cases = append(cases, []string{command, "--config", badConfig, "--help"}, []string{"help", command})
	}
	for _, args := range cases {
		var out bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader(""), &out, &out); err != nil {
			t.Errorf("%v: %v", args, err)
		}
		if out.Len() == 0 {
			t.Errorf("%v: empty help", args)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("help contacted the server")
	}
	if _, err := os.Stat(filepath.Join(root, "codex")); !os.IsNotExist(err) {
		t.Fatal("help installed a skill")
	}
}
