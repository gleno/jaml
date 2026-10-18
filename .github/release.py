#!/usr/bin/env python3
"""Decide the next release tag from conventional commits since the last tag.

feat: -> minor bump, fix: -> patch bump, anything else -> no release.
Reads git state only; writes the chosen tag (if any) to $GITHUB_OUTPUT as `next`.
"""
import os
import re
import subprocess

FEAT = re.compile(r"^feat(\([^)]+\))?!?:")
FIX = re.compile(r"^fix(\([^)]+\))?!?:")


def compute_next(last, subjects):
    if any(FEAT.match(s) for s in subjects):
        bump = "minor"
    elif any(FIX.match(s) for s in subjects):
        bump = "patch"
    else:
        return None, None
    major, minor, patch = (int(p) for p in last[1:].split("."))
    if bump == "minor":
        return f"v{major}.{minor + 1}.0", bump
    return f"v{major}.{minor}.{patch + 1}", bump


def git(*args):
    return subprocess.run(
        ["git", *args], check=True, capture_output=True, text=True
    ).stdout.strip()


def latest_tag():
    tags = [t for t in git("tag", "-l", "v*.*.*").splitlines()
            if re.fullmatch(r"v\d+\.\d+\.\d+", t)]
    tags.sort(key=lambda t: tuple(int(p) for p in t[1:].split(".")))
    return tags[-1] if tags else None


def write(path_var, line):
    path = os.environ.get(path_var)
    if path:
        with open(path, "a") as f:
            f.write(line + "\n")


def main():
    git("fetch", "--tags", "--force", "--quiet")
    last = latest_tag()
    base = last or "v0.0.0"
    commit_range = f"{last}..HEAD" if last else "HEAD"
    subjects = git("log", "--format=%s", commit_range).splitlines()

    print(f"Commits since {base}:")
    for subject in subjects:
        print(f"  {subject}")

    nxt, bump = compute_next(base, subjects)
    if nxt is None:
        print(f"No feat/fix commits since {base} - no release.")
        write("GITHUB_STEP_SUMMARY", f"No release: no `feat`/`fix` commits since {base}.")
        return

    print(f"Bump {bump}: {base} -> {nxt}")
    write("GITHUB_OUTPUT", f"next={nxt}")
    write("GITHUB_OUTPUT", f"bump={bump}")


if __name__ == "__main__":
    main()
