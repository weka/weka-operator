#!/usr/bin/env -S uv run --no-project --with openai-agents
import argparse
import subprocess
import sys
import os
import re
import requests
from agents import Agent, Runner, ModelSettings, OpenAIChatCompletionsModel
from openai import AsyncOpenAI
from typing import Optional, List
import math # Added for ceiling division

MAX_DIFF_SIZE = 16 * 1024  # 16 KB
MAX_FILE_DELTA = 1024      # 1 KB
CHUNK_SIZE = 300           # bytes for large file deltas
GITHUB_API_URL = "https://api.github.com"
REPO = "weka/weka-operator"

GOOGLE_OPENAI_ENDPOINT="https://generativelanguage.googleapis.com/v1beta/openai/"
GEMINI_API_KEY  = os.getenv("GEMINI_API_KEY")

gemini_client = AsyncOpenAI(base_url=GOOGLE_OPENAI_ENDPOINT, api_key=GEMINI_API_KEY)
final_release_notes_model = OpenAIChatCompletionsModel(
    model="gemini-2.5-pro-preview-03-25",
    openai_client=gemini_client
)


class CommitInfo:
    def __init__(self, sha, subject, ctype, pr_url=None, pr_body=None, release_notes=None, ignored=False, pr_number=None):
        self.sha = sha
        self.subject = subject
        self.ctype = ctype
        self.pr_url = pr_url
        self.pr_body = pr_body
        self.release_notes = release_notes
        self.ignored = ignored
        self.pr_number = pr_number

    def __repr__(self):
        return f"CommitInfo(sha={self.sha}, subject={self.subject}, ctype={self.ctype}, pr_url={self.pr_url}, ignored={self.ignored})"

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

def _get_github_headers():
    token = os.environ.get("GITHUB_PAT_TOKEN")
    if not token:
        raise Exception("GITHUB_PAT_TOKEN is not set")
    return {
        "Accept": "application/vnd.github.v3+json",
        "Authorization": f"Bearer {token}",
    }

def fetch_recent_closed_prs(count):
    """Fetch the specified number of most recently updated closed PRs."""
    headers = _get_github_headers()
    prs = []
    per_page = 100 # Max allowed by GitHub
    pages = math.ceil(count / per_page)
    remaining = count

    for page in range(1, pages + 1):
        page_size = min(remaining, per_page)
        url = f"{GITHUB_API_URL}/repos/{REPO}/pulls?state=closed&sort=updated&direction=desc&per_page={page_size}&page={page}"
        try:
            resp = requests.get(url, headers=headers, timeout=20)
            resp.raise_for_status() # Raise an exception for bad status codes
            page_prs = resp.json()
            if not page_prs: # Stop if GitHub returns an empty list
                break
            prs.extend(page_prs)
            remaining -= len(page_prs)
            if remaining <= 0:
                break
        except requests.exceptions.RequestException as e:
            print(f"Error fetching PRs (page {page}): {e}", file=sys.stderr)
            sys.exit(1)
    for pr in prs:
        print(pr['title'])
    return prs

def fetch_pr_body(pr_url):
    """Fetches the body of a specific PR given its API URL."""
    headers = _get_github_headers()
    try:
        resp = requests.get(pr_url, headers=headers, timeout=10)
        resp.raise_for_status()
        return resp.json().get("body", "")
    except requests.exceptions.RequestException as e:
        print(f"Error fetching PR body for {pr_url}: {e}", file=sys.stderr)
        return None # Allow continuing if one PR fetch fails maybe?

def update_pr_body(pr_number, new_body, dry_run=False):
    headers = _get_github_headers()
    url = f"{GITHUB_API_URL}/repos/{REPO}/pulls/{pr_number}"
    if dry_run:
        print(f"---\n[DRY RUN] Would update PR #{pr_number} ({REPO}#{pr_number}) with new body:\n{new_body}\n---")
        return
    try:
        resp = requests.patch(url, headers=headers, json={"body": new_body}, timeout=10)
        resp.raise_for_status()
        print(f"Successfully updated PR #{pr_number}")
    except requests.exceptions.RequestException as e:
        print(f"Failed to update PR #{pr_number}: {e} - {resp.text if 'resp' in locals() else 'N/A'}", file=sys.stderr)

def extract_release_notes_tag(pr_body):
    if not pr_body:
        return None
    match = re.search(r"<release_notes>(.*?)</release_notes>", pr_body, re.DOTALL)
    if match:
        return match.group(1).strip()
    return None

def agent_generate_release_notes(commit_info, commit_show):
    agent = Agent(
        name="commit_release_notes_summarizer",
        model="gpt-4.1-mini",
        instructions=(
            """You are provided with commit metadata and code changes. Generate user-facing release notes for this commit. "
            "If the commit is internal or not user-facing, respond with 'Ignored'.
            Github action changes, linting changes, build system changes, etc are not user-facing changes.
            Do not include in response anything but the release notes text for this specific item, structured as:
            Derrive the type and fill appropriately
            ### Title
            Type: [fix|feature|breaking]
            Description
             """
        )
    )
    input_text = f"Commit: {commit_info.subject}\nType: {commit_info.ctype}\nSHA: {commit_info.sha}\n\n{commit_show}"
    result = Runner.run_sync(agent, input_text)
    # The agent is expected to return the formatted note directly or 'Ignored'
    return result.final_output.strip()

def process_commit(commit, recent_prs, dry_run=False):
    message = get_commit_message(commit)
    ctype = infer_type(message)
    if ctype not in ('fix', 'feature', 'breaking'):
        return None # Skip commits not matching the required types
    subject = message.splitlines()[0]
    commit_info = CommitInfo(sha=commit, subject=subject, ctype=ctype)

    # Find PR by matching title with commit subject
    matched_pr = None
    for pr in recent_prs:
        if pr.get("title") == subject:
            matched_pr = pr
            break

    if matched_pr:
        pr_url = matched_pr["url"]
        pr_number = matched_pr["number"]
        pr_body = matched_pr["body"] if matched_pr["body"] else ""
        
        commit_info.pr_url = pr_url
        commit_info.pr_body = pr_body
        commit_info.pr_number = pr_number
        rn_tag = extract_release_notes_tag(pr_body)

        if rn_tag is not None:
            if rn_tag.lower() == "ignored":
                commit_info.release_notes = "Ignored"
                commit_info.ignored = True
                print(f"Commit {commit[:7]}: Found existing 'Ignored' tag in PR #{pr_number}. Skipping.")
                return commit_info # Already processed and ignored
            else:
                commit_info.release_notes = rn_tag
                print(f"Commit {commit[:7]}: Found existing release notes tag in PR #{pr_number}.")
                return commit_info # Use existing notes

        # No valid <release_notes> tag found in matched PR, generate and update PR
        print(f"Commit {commit[:7]}: No release notes tag in PR #{pr_number}. Generating...")
        commit_show = get_commit_show(commit)
        rn = agent_generate_release_notes(commit_info, commit_show)
        commit_info.release_notes = rn
        if rn.lower() == "ignored":
            commit_info.ignored = True
            print(f"Commit {commit[:7]}: Generated 'Ignored'. Updating PR #{pr_number}.")
        else:
             print(f"Commit {commit[:7]}: Generated release notes. Updating PR #{pr_number}.")

        # Append or replace <release_notes> in PR body
        new_body = pr_body # Already fetched
        tag_to_insert = f"<release_notes>\n{rn}\n</release_notes>"
        if re.search(r"<release_notes>.*?</release_notes>", new_body, re.DOTALL):
            new_body = re.sub(r"<release_notes>.*?</release_notes>", tag_to_insert, new_body, flags=re.DOTALL)
        else:
            new_body = (new_body + "\n\n" + tag_to_insert).strip()
        update_pr_body(pr_number, new_body, dry_run=dry_run)
        return commit_info

    # If we reach here, either no PR was matched OR fetching PR body failed.
    if not matched_pr:
        print(f"Commit {commit[:7]}: No matching PR found by title '{subject}'. Generating release notes without PR context.")
    # Generate release notes without PR interaction
    commit_show = get_commit_show(commit)
    rn = agent_generate_release_notes(commit_info, commit_show)
    commit_info.release_notes = rn
    if rn.lower() == "ignored":
        commit_info.ignored = True
        print(f"Commit {commit[:7]}: Generated 'Ignored' (no PR). Skipping.")
    else:
        print(f"Commit {commit[:7]}: Generated release notes (no PR).")

    return commit_info

def aggregate_release_notes(commit_infos: List[CommitInfo], review_mode=False):
    # Only include non-ignored
    included = [ci for ci in commit_infos if ci and not ci.ignored]
    if not included:
        return "No user-facing changes found in this range."
    
    input_items = []
    for ci in included:
        input_item = "<item>"
        input_item += f"Commit: {ci.sha}:\n {ci.release_notes}"
        if review_mode and ci.pr_number:
            pr_link = f"https://github.com/{REPO}/pull/{ci.pr_number}"
            input_item += f"\nPR: {pr_link}"
        input_item += "</item>"
        input_items.append(input_item)
    
    input_text = "\n\n".join(input_items)
    
    instructions = """You are provided with release notes for multiple commits. 
        Combine and structure them into a user-facing release notes.
        You may merge similar/related commits into a single entry, but preserve all commits SHAs.
        You may drop items if they are not user-facing, with high level of confidence.
        Output in markdown. Each change must be clearly referenced by commit SHA.
        <instructions>
        - Do not wrap pull requests as markdown links, just put links as-is, as a space separated list. Make sure to include all PRs in appropriate place
        - Make sure to include all input items in the output, do not drop any of them, they are separated by <item> tags in the input
        - IF for whatever reason something was dropped - list this items explicitly, in the end of the output, explaining why
        - [Group name] is a category name, it can be "Breaking changes", "Features", "Fixes" in this order
            - If there is no content for the group do not include it  
        <instructions>
        """
    
    instructions += """Output in following format:
        <format>
        # [Group name]
        ### Title
        [Description]
        Commits: [Commit SHA], [Commit SHA], ...
        PRs: PR_LINK, PR_LINK, ... //if any
        </format>
        """
    
    agent = Agent(
        name="final_release_notes_aggregator",
        model=final_release_notes_model,
        instructions=instructions,
        model_settings=ModelSettings(
            max_tokens=65536,
        )
    )

    # print(instructions)
    # print(input_text)
    result = Runner.run_sync(agent, input_text)
    final_output = result.final_output
    # Count unique commit SHAs in the final output
    sha_pattern = r"[0-9a-f]{7,40}"  # SHA-1 hashes (7+ hex chars)
    found_shas = set(re.findall(sha_pattern, final_output))
    expected_shas = set(ci.sha for ci in included)
    if found_shas != expected_shas:
        print(f"WARNING: Number of unique commit SHAs in output ({len(found_shas)}) does not match number of processed commits ({len(expected_shas)}).", file=sys.stderr)
        missing = expected_shas - found_shas
        extra = found_shas - expected_shas
        if missing:
             print(f"  Missing SHAs in output: {', '.join(missing)}", file=sys.stderr)
            # raise Exception("Missing SHAs in output", missing) # Keep as warning for now
        if extra:
             print(f"  Extra SHAs in output: {', '.join(extra)}", file=sys.stderr)
            # raise Exception("Extra SHAs in output", extra) # Keep as warning for now
    return final_output

def validate_commits_against_prs(commits, recent_prs, continue_on_missing=False):
    """
    Validate all commits have matching PRs before proceeding with changelog generation.
    Returns True if validation passes or user chooses to continue, False to abort.
    """
    missing_matches = []
    
    print("Validating all commits have matching PRs...")
    for i, commit in enumerate(commits):
        message = get_commit_message(commit)
        if infer_type(message) not in ('fix', 'feature', 'breaking'):
            continue  # Skip commits of unrecognized types
            
        subject = message.splitlines()[0]
        # Find matching PR
        matched = False
        for pr in recent_prs:
            if pr.get("title") == subject:
                matched = True
                break
                
        if not matched:
            missing_matches.append((commit, subject))
            print(f"WARNING: No matching PR found for commit {commit[:7]}: '{subject}'", file=sys.stderr)
    
    if missing_matches:
        print(f"\n⚠️  Found {len(missing_matches)} commits without matching PRs:", file=sys.stderr)
        for commit, subject in missing_matches:
            print(f"  - {commit[:10]}: {subject}", file=sys.stderr)
            
        if not continue_on_missing:
            response = input("\nDo you want to continue anyway? (y/N): ").strip().lower()
            if response != 'y':
                print("Aborting changelog generation. Please fix missing PR links.")
                return False
                
    return True

def main():
    parser = argparse.ArgumentParser(description='Advanced changelog generator')
    parser.add_argument('--from', dest='gfrom', help='Tag or commit to start from (exclusive)')
    parser.add_argument('--to', dest='gto', default='HEAD', help='Tag or commit to end at (inclusive)')
    parser.add_argument('--dry-run', action='store_true', help='Do not update PRs, just print what would be updated')
    parser.add_argument('--force', action='store_true', help='Continue even if some commits have no matching PRs')
    parser.add_argument('--review', action='store_true', help='Include PR links in the output for review purposes')
    args = parser.parse_args()

    gfrom = args.gfrom or get_latest_tag()
    gto = args.gto
    if not gfrom:
        print('No --from specified and no tags found in repo.', file=sys.stderr)
        sys.exit(1)

    commits = get_commit_range(gfrom, gto)
    if not commits:
        print(f'No commits found in range {gfrom}..{gto}', file=sys.stderr)
        sys.exit(0)

    # Fetch recent PRs once
    num_commits = len(commits)
    num_prs_to_fetch = math.ceil(num_commits * 2)
    print(f"Fetching {num_prs_to_fetch} recent closed PRs...")
    recent_prs = fetch_recent_closed_prs(num_prs_to_fetch)
    print(f"Fetched {len(recent_prs)} PRs.")
    
    # Validate all commits have matching PRs before proceeding
    if not validate_commits_against_prs(commits, recent_prs, continue_on_missing=args.force):
        sys.exit(1)

    commit_infos = []
    processed_commit_count = 0
    for commit in commits:
        processed_commit_count += 1
        print(f"Processing commit {processed_commit_count}/{num_commits}: {commit[:7]}...")
        ci = process_commit(commit, recent_prs, dry_run=args.dry_run)
        # process_commit now handles the case where no matching PR is found
        if ci:
            commit_infos.append(ci)
        # Removed early exit if PR not found, process_commit handles it

    print("Aggregating release notes...")
    final_output = aggregate_release_notes(commit_infos, review_mode=args.review)
    print("\n=== FINAL CHANGELOG ===\n")
    print(final_output)

if __name__ == '__main__':
    main() 