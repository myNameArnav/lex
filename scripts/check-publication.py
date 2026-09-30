#!/usr/bin/env python3
"""Check publishable source and reachable branch history without echoing PII.

This is a targeted privacy guard, not a replacement for manual review or Gitleaks.
T3/application-internal refs are not publication branches. Do not push --mirror.
"""
import pathlib
import re
import subprocess
import sys


def git(*args):
    return subprocess.check_output(["git", *args])


PATTERNS = {
    "personal home path": re.compile(rb"/(?:Users|home)/[A-Za-z0-9_.-]+/"),
    "private tailnet address": re.compile(rb"\b[A-Za-z0-9.-]+\.ts\.net\b"),
    "private key": re.compile(rb"-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----"),
}
PRIVATE_NAMES = {".env", ".DS_Store", "lex.db", "lex.db-wal", "lex.db-shm"}
PRIVATE_SUFFIXES = {".db", ".sqlite", ".sqlite3", ".key", ".pem", ".log"}
violations = []


def check(path, content, location):
    name = pathlib.PurePosixPath(path).name
    if name in PRIVATE_NAMES or (name.startswith(".env.") and name != ".env.example") or pathlib.PurePosixPath(path).suffix in PRIVATE_SUFFIXES:
        violations.append((location, path, "private runtime/configuration file"))
    if path.startswith(("data/", "build/")):
        violations.append((location, path, "runtime/build artifact"))
    for label, pattern in PATTERNS.items():
        if pattern.search(content):
            violations.append((location, path, label))
    for match in re.finditer(rb"\b[A-Za-z0-9._%+-]+@([A-Za-z0-9.-]+\.[A-Za-z]{2,})\b", content):
        if match.group(1).lower() not in {b"example.com", b"example.org", b"example.net", b"example.invalid"}:
            violations.append((location, path, "non-example email address"))


paths = git("ls-files", "--cached", "--others", "--exclude-standard", "-z").decode().split("\0")
for path in sorted(set(paths) - {""}):
    file = pathlib.Path(path)
    if file.is_file():
        check(path, file.read_bytes(), "working tree")

# Branches and tags are the refs that a normal public push can publish.
refs = git("for-each-ref", "--format=%(refname)", "refs/heads", "refs/tags").decode().splitlines()
seen = set()
for ref in refs:
    for commit in git("rev-list", ref).decode().splitlines():
        if commit in seen:
            continue
        seen.add(commit)
        for entry in git("ls-tree", "-rz", commit).split(b"\0"):
            if not entry:
                continue
            metadata, path = entry.split(b"\t", 1)
            _, kind, object_id = metadata.split()
            if kind == b"blob":
                check(path.decode(), git("cat-file", "blob", object_id.decode()), commit[:12])
        identities = git("show", "-s", "--format=%an%n%ae%n%cn%n%ce", commit).decode().splitlines()
        if identities != ["Lex contributors", "contributors@example.invalid", "Lex contributors", "contributors@example.invalid"]:
            violations.append((commit[:12], "commit metadata", "non-neutral author/committer identity"))

if violations:
    for location, path, label in sorted(set(violations)):
        print(f"FAIL {location}: {path}: {label}", file=sys.stderr)
    sys.exit(1)
print(f"Publication privacy checks passed ({len(set(paths) - {''})} source files, {len(seen)} commits).")
