package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	bundled "github.com/felixfeng33/taskhub/skills"
)

const skillHelp = `Usage:
  taskhub skill                        Print the bundled SKILL.md; no server needed
  taskhub skill install [--dir PATH] [--force]

Install $taskhub for natural-language task management in Codex.
Default destination: $CODEX_HOME/skills/taskhub, or ~/.codex/skills/taskhub.
--dir is the exact skill directory, not its parent. --force replaces differing
SKILL.md and agents/openai.yaml files; other files are kept. Symlinks are refused.
An identical existing installation is left unchanged. Review local edits before --force.

After installation, use $taskhub in a Codex task. If discovery has not refreshed,
open a new Codex task. The skill uses this machine's existing taskhub connection.
`

func runSkill(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		data, err := bundled.Files.ReadFile("taskhub/SKILL.md")
		if err != nil {
			return err
		}
		_, err = stdout.Write(data)
		return err
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(stdout, skillHelp)
		return err
	}
	if args[0] != "install" {
		return errors.New("usage: taskhub skill [install]; use taskhub skill --help")
	}
	f := flag.NewFlagSet("skill install", flag.ContinueOnError)
	f.SetOutput(stderr)
	setUsage(f, skillHelp)
	dir := f.String("dir", "", "exact destination skill directory")
	force := f.Bool("force", false, "replace differing bundled files after reviewing local edits")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("skill install takes no positional arguments")
	}
	if *dir == "" {
		codexDir := os.Getenv("CODEX_HOME")
		if codexDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			codexDir = filepath.Join(home, ".codex")
		}
		*dir = filepath.Join(codexDir, "skills", "taskhub")
	}
	root, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	for _, path := range []string{root, filepath.Join(root, "agents")} {
		if info, err := os.Lstat(path); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("skill directory must be a real directory: %s", path)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	type entry struct {
		path string
		data []byte
	}
	var writes []entry
	for _, name := range []string{"SKILL.md", "agents/openai.yaml"} {
		data, err := bundled.Files.ReadFile("taskhub/" + name)
		if err != nil {
			return err
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("refusing to replace non-regular file: %s", path)
			}
			existing, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Equal(existing, data) {
				continue
			}
			if !*force {
				return fmt.Errorf("%s differs; review local edits, then use --force or another --dir", path)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		writes = append(writes, entry{path, data})
	}
	// Check every existing file before writing any of the bundle.
	for _, item := range writes {
		if err := os.MkdirAll(filepath.Dir(item.path), 0755); err != nil {
			return err
		}
		if err := writeSkillFile(item.path, item.data); err != nil {
			return err
		}
	}
	if len(writes) == 0 {
		fmt.Fprintf(stdout, "$taskhub is already installed at %s\n", root)
	} else {
		fmt.Fprintf(stdout, "Installed $taskhub at %s\n", root)
	}
	return nil
}

func writeSkillFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".taskhub-skill-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
