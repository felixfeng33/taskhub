#!/usr/bin/env python3
"""Refresh license notices for modules linked into the CLI."""
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
raw = subprocess.check_output(["go", "list", "-deps", "-json", "./cmd/taskhub"], cwd=ROOT, text=True)
decoder = json.JSONDecoder()
modules = {}
while raw.strip():
    raw = raw.lstrip()
    package, end = decoder.raw_decode(raw)
    raw = raw[end:]
    module = package.get("Module", {})
    if module.get("Dir") and not module.get("Main"):
        modules[module["Path"]] = module

go_root = Path(subprocess.check_output(["go", "env", "GOROOT"], text=True).strip())
go_license = next((p for p in [go_root / "LICENSE", go_root.parent / "LICENSE"] if p.exists()), None)
if go_license is None:
    raise SystemExit("Cannot find Go's LICENSE")
chunks = ["Third-party notices\n===================", "Go runtime and standard library\n\n" + go_license.read_text()]
for name, module in sorted(modules.items()):
    files = sorted(p for p in Path(module["Dir"]).iterdir() if p.is_file() and p.name.upper().startswith(("LICENSE", "COPYING", "NOTICE")))
    if not files:
        raise SystemExit("Missing license for " + name)
    chunks.append("\n" + name + " " + module["Version"] + "\n" + "-" * 60)
    chunks.extend(p.name + "\n\n" + p.read_text(errors="replace") for p in files)
(ROOT / "THIRD_PARTY_NOTICES").write_text("\n\n".join(chunks).rstrip() + "\n")
print(f"Generated notices for Go and {len(modules)} linked modules")
