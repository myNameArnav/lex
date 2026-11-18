#!/usr/bin/env python3
"""Check publishable source and reachable branch history without echoing PII.

This is a targeted privacy guard, not a replacement for manual review or Gitleaks.
T3/application-internal refs are not publication branches. Do not push --mirror.

With no arguments it checks every commit on every branch and tag. With
--since BASE (CI passes the pull request's base branch) it checks only the
commits not already in BASE, which BASE's own checks have covered.
"""
import argparse
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
URL_CREDENTIALS = re.compile(rb"[A-Za-z][A-Za-z0-9+.-]*://[A-Za-z0-9._%+-]*:\Z")
violations = []


def path_labels(path):
    name = pathlib.PurePosixPath(path).name
    labels = []
    if name in PRIVATE_NAMES or (name.startswith(".env.") and name != ".env.example") or pathlib.PurePosixPath(path).suffix in PRIVATE_SUFFIXES:
        labels.append("private runtime/configuration file")
    if path.startswith(("data/", "build/")):
        labels.append("runtime/build artifact")
    return labels


def content_labels(content):
    labels = [label for label, pattern in PATTERNS.items() if pattern.search(content)]
    for match in re.finditer(rb"\b[A-Za-z0-9._%+-]+@([A-Za-z0-9.-]+\.[A-Za-z]{2,})\b", content):
        # "scheme://user:secret@host" is a URL's credentials, not an address.
        if URL_CREDENTIALS.search(content[max(0, match.start() - 256):match.start()]):
            continue
        # FFmpeg's public mailing list is compiled into the bug-report
        # messages of the browser's audio converter and remuxer
        # (web/static/vendor/lexaudio, web/static/vendor/lexremux).
        if match.group(0).lower() == b"ffmpeg-devel@ffmpeg.org":
            continue
        if match.group(1).lower() not in {b"example.com", b"example.org", b"example.net", b"example.invalid"}:
            labels.append("non-example email address")
    return labels


def check(path, content, location):
    for label in path_labels(path) + content_labels(content):
        violations.append((location, path, label))


class Objects:
    """Reads objects through one long-running `git cat-file --batch`."""

    def __init__(self):
        self.proc = subprocess.Popen(["git", "cat-file", "--batch"], stdin=subprocess.PIPE, stdout=subprocess.PIPE)

    def read(self, object_id):
        self.proc.stdin.write(object_id + b"\n")
        self.proc.stdin.flush()
        header = self.proc.stdout.readline().split()
        if len(header) != 3:
            raise SystemExit(f"git cat-file: cannot read {object_id.decode()}")
        data = self.proc.stdout.read(int(header[2]))
        self.proc.stdout.read(1)  # the newline after each object
        return data


paths = git("ls-files", "--cached", "--others", "--exclude-standard", "-z").decode().split("\0")
for path in sorted(set(paths) - {""}):
    file = pathlib.Path(path)
    if file.is_file():
        check(path, file.read_bytes(), "working tree")

parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
parser.add_argument("--since", metavar="BASE", help="check only the commits reachable from HEAD but not from BASE")
args = parser.parse_args()
if args.since:
    if subprocess.run(["git", "rev-parse", "--verify", "--quiet", f"{args.since}^{{commit}}"], stdout=subprocess.DEVNULL).returncode:
        sys.exit(f"--since {args.since}: no such commit (fetch it first)")
    revisions = [f"{args.since}..HEAD"]
else:
    # Branches and tags are the refs that a normal public push can publish.
    revisions = git("for-each-ref", "--format=%(refname)", "refs/heads", "refs/tags").decode().splitlines()
objects = Objects()
hash_len = 32 if git("rev-parse", "--show-object-format").strip() == b"sha256" else 20
tree_files = {}  # tree id -> [(path, blob id)]; trees are shared between commits


def files(tree):
    """Every blob (files and symlinks) under tree, with its path."""
    if tree not in tree_files:
        out, data, i = [], objects.read(tree), 0
        while i < len(data):
            space, nul = data.index(b" ", i), data.index(b"\0", i)
            mode, name = data[i:space], data[space + 1:nul]
            object_id = data[nul + 1:nul + 1 + hash_len].hex().encode()
            i = nul + 1 + hash_len
            if mode == b"40000":
                out += [(name + b"/" + path, blob) for path, blob in files(object_id)]
            elif mode != b"160000":  # submodule commits aren't in this repository
                out.append((name, object_id))
        tree_files[tree] = out
    return tree_files[tree]


# One git log lists every commit with its tree and identities.
seen = set()
checked = set()  # (path, blob): a file unchanged since an earlier commit isn't checked again
labels_of_blob, labels_of_path = {}, {}
log = git("log", "-z", "--format=%H%x01%T%x01%an%x01%ae%x01%cn%x01%ce", *revisions, "--") if revisions else b""
for record in log.split(b"\0"):
    if not record:
        continue
    commit, tree, *identities = record.decode().split("\x01")
    seen.add(commit)
    for path, blob in files(tree.encode()):
        if (path, blob) in checked:
            continue
        checked.add((path, blob))
        path = path.decode()
        if path not in labels_of_path:
            labels_of_path[path] = path_labels(path)
        if blob not in labels_of_blob:
            labels_of_blob[blob] = content_labels(objects.read(blob))
        for label in labels_of_path[path] + labels_of_blob[blob]:
            violations.append((commit[:12], path, label))
    # Any name is fine (e.g. a GitHub username), but the recorded emails must
    # not be personal: the neutral project address, a GitHub no-reply
    # address, or GitHub itself as committer of a web merge.
    # (Addresses are split so this file doesn't trip its own email scan.)
    def public_email(email):
        return (email == "contributors" "@example.invalid"
                or email == "noreply" "@" "github.com"
                or email.endswith("@" "users.noreply.github.com"))
    if not (public_email(identities[1]) and public_email(identities[3])):
        violations.append((commit[:12], "commit metadata", "author/committer email isn't a GitHub no-reply address"))

# The Go version named in the docs and the Dockerfile must match go.mod,
# which a local toolchain has to satisfy (GOTOOLCHAIN=local fails otherwise).
go_mod = re.search(r"^go (\S+)$", pathlib.Path("go.mod").read_text(), re.M).group(1)
for path, pattern in [("README.md", r"Go \*\*(\S+) or newer\*\*"),
                      ("docs/install.md", r"Go \*\*(\S+) or newer\*\*"),
                      ("CONTRIBUTING.md", r"Use Go (\S+) or newer"),
                      ("Dockerfile", r"golang:([\d.]+)-")]:
    found = re.findall(pattern, pathlib.Path(path).read_text())
    if not found:
        violations.append(("working tree", path, "Go version not found"))
    for version in found:
        if version != go_mod:
            violations.append(("working tree", path, f"names Go {version}, go.mod requires {go_mod}"))

if violations:
    for location, path, label in sorted(set(violations)):
        print(f"FAIL {location}: {path}: {label}", file=sys.stderr)
    sys.exit(1)
print(f"Publication privacy checks passed ({len(set(paths) - {''})} source files, {len(seen)} commits).")
