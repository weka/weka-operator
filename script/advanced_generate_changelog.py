#!/usr/bin/env -S uv run --no-project --with openai-agents
import argparse
import subprocess
import sys
import os

from agents import Agent, Runner




MAX_DIFF_SIZE = 16 * 1024  # 16 KB
MAX_FILE_DELTA = 1024      # 1 KB
CHUNK_SIZE = 300           # bytes for large file deltas

def run_git(cmd, **kwargs):
    result = subprocess.run(['git'] + cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs)
    if result.returncode != 0:
        print(f"Error running git {' '.join(cmd)}: {result.stderr.decode()}", file=sys.stderr)
        sys.exit(1)
    return result.stdout.decode()

def get_latest_tag():
    tags = run_git(['tag', '--sort=-creatordate']).splitlines()
    return tags[0] if tags else None

def get_commit_range(gfrom, gto):
    commits = run_git(['rev-list', '--reverse', f'{gfrom}..{gto}']).splitlines()
    return commits

def get_commit_message(commit):
    return run_git(['log', '--format=%B', '-n', '1', commit]).strip()

def get_commit_show(commit):
    return run_git(['show', '--format=fuller', commit])

def get_commit_show_stat(commit):
    return run_git(['show', '--stat', '--format=fuller', commit])

def get_commit_files_and_deltas(commit):
    stat = run_git(['show', '--numstat', '--format=', commit])
    files = []
    for line in stat.splitlines():
        parts = line.split('\t')
        if len(parts) == 3:
            added, deleted, path = parts
            try:
                delta = int(added) + int(deleted)
            except ValueError:
                delta = 0
            files.append((path, delta))
    return files

def get_file_diff(commit, path):
    return run_git(['show', f'{commit}:{path}'])

def get_file_patch(commit, path):
    return run_git(['show', commit, '--', path])

def summarize_large_file_diff(commit, path):
    content = get_file_patch(commit, path)
    if len(content) < CHUNK_SIZE * 2:
        return content
    return content[:CHUNK_SIZE] + '\n...\n' + content[-CHUNK_SIZE:]

def infer_type(message):
    msg = message.lower()
    if 'breaking' in msg.split(':')[0] or 'breaking change' in msg:
        return 'breaking'
    if msg.startswith('fix') or 'fix' in msg.split(':')[0]:
        return 'fix'
    if msg.startswith('feat') or 'feature' in msg.split(':')[0]:
        return 'feature'
    return 'other'

def generate_release_notes(gfrom, gto):
    commits = get_commit_range(gfrom, gto)
    if not commits:
        return
    for commit in commits:
        message = get_commit_message(commit)
        ctype = infer_type(message)
        if ctype not in ('fix', 'feature', 'breaking'):
            continue
        subject = message.splitlines()[0]
        output = []
        output.append(f"Proposed name: {subject}")
        output.append(f"Proposed type: {ctype}")
        output.append(f"Original commit:\n<commit>")

        show = get_commit_show(commit)
        diff_start = show.find('diff --git')
        if diff_start == -1:
            output.append(show)
            output.append("</commit>")
            yield '\n'.join(output)
            continue
        meta = show[:diff_start]
        diff = show[diff_start:]
        if len(diff.encode()) <= MAX_DIFF_SIZE:
            output.append(meta + diff)
        else:
            output.append(meta)
            output.append('--- DIFF TOO LARGE, SHOWING FILE STATS AND SUMMARIES ---')
            stat = get_commit_show_stat(commit)
            output.append(stat)
            files = get_commit_files_and_deltas(commit)
            for path, delta in files:
                output.append(f'File: {path} (delta: {delta} lines)')
                if delta < MAX_FILE_DELTA:
                    patch = get_file_patch(commit, path)
                    output.append(patch)
                else:
                    summary = summarize_large_file_diff(commit, path)
                    output.append(summary)
        output.append("</commit>")
        output.append("\n" + "="*80 + "\n")
        yield '\n'.join(output)

def main():
    parser = argparse.ArgumentParser(description='Advanced changelog generator')
    parser.add_argument('--from', dest='gfrom', help='Tag or commit to start from (exclusive)')
    parser.add_argument('--to', dest='gto', default='HEAD', help='Tag or commit to end at (inclusive)')
    args = parser.parse_args()

    gfrom = args.gfrom or get_latest_tag()
    gto = args.gto
    if not gfrom:
        print('No --from specified and no tags found in repo.', file=sys.stderr)
        sys.exit(1)

    found = False
    notes = []
    for note in generate_release_notes(gfrom, gto):
        found = True
        notes.append(note)
        print(note)
    summarize_notes(" ".join(notes))
    if not found:
        print(f'No commits found in range {gfrom}..{gto}', file=sys.stderr)
        sys.exit(0)

def summarize_notes(notes):
    root_agent = Agent(
        name="release_notes_summarizer",
        model="gpt-4.1-mini",
        instructions=(
            """Your task is to transform provided release notes that contain actual code changes into a better format.
            You are provided with "Proposed name", "Proposed type", and "Original commit".
            You should consider proposed type and split into Breaking Feature, Feature and Fix
            Sometimes what seems to be a feature will have proposed type Fix. This might be because we did not want to bump semver
            Consider such as features, if they clearly add new functionality.
            Consider actual code changes to craft better release notes. 
            Release notes should be in markdown format, have separate block for breaking changes, features and fixes.
            Each change should have title and description. Try to be concise, but do include major details, if there is a new user-facing value - call out it explicitly with what are the default values.
            Your output is a user-facing documentation, do not expose internal, but callout any changes that are relevant to users and what that might mean to them.
            Do not include empty sections.
            Do not include environment variables as they are not user-facing.
            Call out sections as ## Features and ## Fixes
            Do not categorie changes and have a flat list of title and description, title can be a heading ###
            Do not include "marketing stuff", for example "providing enhanced observability" is redundant if what new metrics are added is already mentioned
            Include commit hash in the description, as a line after the description. Commit hash reference might contain multiple commits, if changes are related and aggregated in description.
            Do not include "CI/CD" related changes, even if they are marked as fix. Github-action related changed are CI/CD related. 
            Do not include internals, like submodule updates. If it had a purpose as a fix - describe the fix, do not mention what files or submodules were changed.
            Format titles as a proper english, i.e only first word is capitalized.
            """
            )
    )
    result = Runner.run_sync(root_agent, notes)
    print(result.final_output)



if __name__ == '__main__':
    main() 