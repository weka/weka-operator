#!/usr/bin/env -S uv run --no-project --with openai-agents
import argparse
import asyncio
# Removed requests import, handled by GitHubClient
import logging
import math  # Added for ceiling division
import re
import subprocess
import sys
from typing import List

from agents import Agent, Runner, ModelSettings

from github import GitHubClient, GitHubError  # Import the new client
from release_notes_utils import infer_type, generate_release_notes  # Import shared utils

MAX_DIFF_SIZE = 16 * 1024  # 16 KB - Maximum total diff size before summarizing individual files
MAX_FILE_DELTA = 1024  # 1 KB - Maximum size for a single file's diff before summarizing it
CHUNK_SIZE = 300  # bytes for summarizing large file deltas
# GITHUB_API_URL and REPO are now handled by GitHubClient
# REPO = "weka/weka-operator" # Define repo name for client initialization

# --- AI Model Setup for Aggregation ---
# Individual commit notes generation model is now in release_notes_utils.py
# Keep the aggregation model setup here if it's different or specifically for this script.

# Set up logging
logger = logging.getLogger(__name__)

from util_gemini import gemini_pro
final_release_notes_model = gemini_pro

# --- Commit Info Class ---
class CommitInfo:
    def __init__(self, sha, subject, ctype, pr_url=None, pr_body=None, release_notes=None, ignored=False,
                 pr_number=None):
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
        logger.error(f"Error running git {' '.join(cmd)}: {result.stderr.decode()}")
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


def summarize_large_file_diff(file_patch_content):
    """Summarizes a large file patch content."""
    # Assumes file_patch_content is already fetched
    if len(file_patch_content.encode('utf-8')) < CHUNK_SIZE * 2:  # Check bytes
        return file_patch_content
    # Ensure we handle potential multi-byte characters correctly if slicing bytes
    # For simplicity, let's assume CHUNK_SIZE is large enough or content is mostly ASCII
    # A more robust solution might involve byte-level slicing with care for character boundaries
    return file_patch_content[:CHUNK_SIZE] + '\n...\n' + file_patch_content[-CHUNK_SIZE:]


# infer_type is now imported from release_notes_utils

# Removed _get_github_headers, fetch_recent_closed_prs, fetch_pr_body, update_pr_body
# These are now handled by GitHubClient

def extract_release_notes_tag(pr_body):
    if not pr_body:
        return None
    match = re.search(r"<release_notes>(.*?)</release_notes>", pr_body, re.DOTALL)
    if match:
        return match.group(1).strip()
    return None


# agent_generate_release_notes is replaced by the imported generate_release_notes

def process_commit(commit, recent_prs, github_client: GitHubClient, repo_name: str, dry_run=False):
    message = get_commit_message(commit)
    subject = message.splitlines()[0]
    # Use imported infer_type
    ctype = infer_type(message)

    # Immediately ignore commits that are not fix, feature, or breaking
    if ctype not in ('fix', 'feature', 'breaking'):
        logger.info(f"Commit {commit[:7]} type '{ctype}' is not fix/feature/breaking. Marking as ignored.")
        # Create CommitInfo marked as ignored and return
        return CommitInfo(sha=commit, subject=subject, ctype=ctype, ignored=True, release_notes="Ignored (type)")

    # If type is valid, proceed with creating CommitInfo and finding PR
    commit_info = CommitInfo(sha=commit, subject=subject, ctype=ctype)

    # Find PR by matching title with commit subject
    matched_pr = None
    for pr in recent_prs:
        if pr.get("title") == subject:
            matched_pr = pr
            break

    if matched_pr:
        # pr_url = matched_pr["url"] # URL is less commonly needed now
        pr_number = matched_pr["number"]
        # Fetch body using client if needed, but it's often in the fetched PR list already
        pr_body = matched_pr.get("body", "") or ""  # Use .get with fallback

        # commit_info.pr_url = pr_url # Store number instead of API URL
        commit_info.pr_body = pr_body
        commit_info.pr_number = pr_number
        rn_tag = extract_release_notes_tag(pr_body)

        if rn_tag is not None:
            if rn_tag.lower() == "ignored":
                commit_info.release_notes = "Ignored"
                commit_info.ignored = True
                logger.info(f"Commit {commit[:7]}: Found existing 'Ignored' tag in PR #{pr_number}. Skipping.")
                return commit_info  # Already processed and ignored
            else:
                commit_info.release_notes = rn_tag
                logger.info(f"Commit {commit[:7]}: Found existing release notes tag in PR #{pr_number}.")
                return commit_info  # Use existing notes

        # No valid <release_notes> tag found in matched PR, generate and update PR
        logger.info(f"Commit {commit[:7]}: No release notes tag in PR #{pr_number}. Preparing diff and generating...")

        # --- Diff Preparation Logic ---
        commit_show_content = get_commit_show(commit)
        diff_for_generation = ""
        if len(commit_show_content.encode('utf-8')) > MAX_DIFF_SIZE:
            logger.debug(f"Commit {commit[:7]}: Diff size exceeds {MAX_DIFF_SIZE} bytes. Summarizing individual files.")
            files_deltas = get_commit_files_and_deltas(commit)
            diff_parts = []
            for path, delta in files_deltas:
                file_patch = get_file_patch(commit, path)
                content_to_include = ""
                if len(file_patch.encode('utf-8')) < MAX_FILE_DELTA:
                    content_to_include = file_patch
                    logger.debug(f"  - Including full diff for {path} ({len(file_patch.encode('utf-8'))} bytes)")
                else:
                    content_to_include = summarize_large_file_diff(file_patch)
                    logger.debug(
                        f"  - Summarizing diff for {path} ({len(file_patch.encode('utf-8'))} bytes > {MAX_FILE_DELTA})")
                diff_parts.append(f'<file path="{path}">\n{content_to_include}\n</file>')
            diff_for_generation = "\n".join(diff_parts)
        else:
            logger.debug(
                f"Commit {commit[:7]}: Diff size within limit ({len(commit_show_content.encode('utf-8'))} bytes). Using full diff.")
            diff_for_generation = commit_show_content
        # --- End Diff Preparation ---

        # Use imported generate_release_notes, passing PR body as original_body and prepared diff
        rn = generate_release_notes(commit_info.subject, commit_info.ctype, commit_info.pr_body, diff_for_generation)
        commit_info.release_notes = rn
        if rn == "Ignored":  # Check for exact "Ignored" string
            commit_info.ignored = True
            logger.info(f"Commit {commit[:7]}: Generated 'Ignored'. Updating PR #{pr_number}.")
        else:
            logger.info(f"Commit {commit[:7]}: Generated release notes. Updating PR #{pr_number}.")

        # Append or replace <release_notes> in PR body
        new_body = pr_body  # Already fetched
        tag_to_insert = f"<release_notes>\n{rn}\n</release_notes>"
        if re.search(r"<release_notes>.*?</release_notes>", new_body, re.DOTALL):
            new_body = re.sub(r"<release_notes>.*?</release_notes>", tag_to_insert, new_body, flags=re.DOTALL)
        else:
            new_body = (new_body + "\n\n" + tag_to_insert).strip()

        # Use GitHubClient to update PR body
        if dry_run:
            logger.info(
                f"---\n[DRY RUN] Would update PR #{pr_number} ({repo_name}#{pr_number}) with new body:\n{new_body}\n---")
        else:
            try:
                github_client.update_pr_body(pr_number, new_body)
                # logger info/error messages are handled within the client method
            except GitHubError as e:
                # Log error and potentially continue, or re-raise depending on desired strictness
                logger.error(f"Failed to update PR #{pr_number} via client: {e}")
                # Decide if we should return commit_info even if update failed
                # return commit_info # Or maybe return None or raise

        return commit_info

    # If we reach here, no PR was matched by title.
    if not matched_pr:
        logger.info(
            f"Commit {commit[:7]}: No matching PR found by title '{subject}'. Preparing diff and generating release notes without PR context.")

        # --- Diff Preparation Logic (Duplicated for no-PR case) ---
        commit_show_content = get_commit_show(commit)
        diff_for_generation = ""
        if len(commit_show_content.encode('utf-8')) > MAX_DIFF_SIZE:
            logger.debug(f"Commit {commit[:7]}: Diff size exceeds {MAX_DIFF_SIZE} bytes. Summarizing individual files.")
            files_deltas = get_commit_files_and_deltas(commit)
            diff_parts = []
            for path, delta in files_deltas:
                file_patch = get_file_patch(commit, path)
                content_to_include = ""
                if len(file_patch.encode('utf-8')) < MAX_FILE_DELTA:
                    content_to_include = file_patch
                    logger.debug(f"  - Including full diff for {path} ({len(file_patch.encode('utf-8'))} bytes)")
                else:
                    content_to_include = summarize_large_file_diff(file_patch)
                    logger.debug(
                        f"  - Summarizing diff for {path} ({len(file_patch.encode('utf-8'))} bytes > {MAX_FILE_DELTA})")
                diff_parts.append(f'<file path="{path}">\n{content_to_include}\n</file>')
            diff_for_generation = "\n".join(diff_parts)
        else:
            logger.debug(
                f"Commit {commit[:7]}: Diff size within limit ({len(commit_show_content.encode('utf-8'))} bytes). Using full diff.")
            diff_for_generation = commit_show_content
        # --- End Diff Preparation ---

    # Generate release notes without PR interaction, using prepared diff
    # Use imported generate_release_notes, passing the full commit message as original_body
    rn = generate_release_notes(commit_info.subject, commit_info.ctype, message, diff_for_generation)
    commit_info.release_notes = rn
    if rn == "Ignored":  # Check for exact "Ignored" string
        commit_info.ignored = True
        logger.info(f"Commit {commit[:7]}: Generated 'Ignored' (no PR).")
    else:
        logger.info(f"Commit {commit[:7]}: Generated release notes (no PR).")

    return commit_info


def aggregate_release_notes(commit_infos: List[CommitInfo], review_mode=False, abort_on_miss=False):
    # Only include non-ignored
    included = [ci for ci in commit_infos if ci and not ci.ignored]
    if not included:
        return "No user-facing changes found in this range."

    # Get repo name from the first commit info's PR if available, needed for links
    # This assumes all PRs are from the same repo, which should be the case here.
    repo_name = None
    if included and included[0].pr_number:
        # We need the client's repo_full_name to construct the link correctly
        # This requires passing the client or its repo name down
        # For now, let's assume a fixed repo name for link generation,
        # but ideally, this should come from the client instance used.
        # TODO: Pass repo_name explicitly or get from client if possible
        repo_name_for_links = "weka/weka-operator"  # Hardcoded for now, replace if needed
        logger.warning(f"Using hardcoded repo name '{repo_name_for_links}' for PR links in aggregation.")

    input_items = []
    for ci in included:
        input_item = "<item>"
        input_item += f"Commit: {ci.sha[:7]}:\n {ci.release_notes}"
        if review_mode and ci.pr_number and repo_name_for_links:
            pr_link = f"https://github.com/{repo_name_for_links}/pull/{ci.pr_number}"
            input_item += f"\nPR: {pr_link}"
        input_item += "</item>"
        input_items.append(input_item)

    input_text = "\n\n".join(input_items)

    instructions = """You are provided with release notes for multiple commits. 
        Combine and structure them into a user-facing release notes.
        You may merge similar/related commits into a single entry, but preserve all commits SHAs.
        You may drop items if they are not user-facing, with high level of confidence.
        Output in markdown. Each change must be clearly referenced by commit SHA.
        Consolidate Reverts/Disable of features into a single entry, but keep references. Rely on PR number to establish if revert was done before or after feature was introduced. If there are newer(by PR number) PRs that mention the feature after it was reverted/disabled - assume that feature was re-enabled. If conclusion is that feature is disabled at final state - mention all relevant PRs as dropped 
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
        # model="o4-mini", # Example alternative
        model=final_release_notes_model,  # Use the aggregator model defined above
        instructions=instructions,
        model_settings=ModelSettings(
            max_tokens=64000,
            # reasoning=dict(
            # effort="high",
            # ),
        )
    )

    # logger.debug(instructions)
    # logger.debug(input_text)
    result = asyncio.run(Runner.run(agent, input_text))
    final_output = result.final_output

    # Validate commits (SHAs)
    sha_pattern = r"[0-9a-f]{7,40}"  # SHA-1 hashes (7+ hex chars)
    found_shas = set(sha[:7] for sha in re.findall(sha_pattern, final_output))
    expected_shas = set(ci.sha[:7] for ci in included)
    if found_shas != expected_shas:
        logger.warning(
            f"Number of unique commit SHAs in output ({len(found_shas)}) does not match number of processed commits ({len(expected_shas)}).")
        missing = expected_shas - found_shas
        extra = found_shas - expected_shas
        if missing:
            msg = f"Missing SHAs in output: {', '.join(missing)}"
            if abort_on_miss:
                raise Exception(msg)
            logger.warning(msg)
        if extra:
            msg = f"Extra SHAs in output: {', '.join(extra)}"
            logger.warning(msg)
            if abort_on_miss:
                raise Exception(msg)

    # Validate PR links - Requires repo name used during generation
    # TODO: Use the actual repo name used for link generation
    repo_name_for_links = "weka/weka-operator"  # Hardcoded for now
    pr_pattern = fr"https://github\.com/{repo_name_for_links}/pull/\d+"
    found_prs = set(re.findall(pr_pattern, final_output))
    expected_prs = set()
    for ci in included:
        if ci.pr_number:
            expected_prs.add(f"https://github.com/{repo_name_for_links}/pull/{ci.pr_number}")

    if found_prs != expected_prs:
        logger.warning(f"PR link validation mismatch (using repo '{repo_name_for_links}'):")
        logger.warning(f"  Expected {len(expected_prs)} PR links, found {len(found_prs)}.")
        missing_prs = expected_prs - found_prs
        extra_prs = found_prs - expected_prs
        if missing_prs:
            logger.warning(f"  Missing PR links in output: {', '.join(missing_prs)}")
        if extra_prs:
            logger.warning(f"  Extra PR links in output: {', '.join(extra_prs)}")

    return final_output


def validate_commits_against_prs(commits, recent_prs, continue_on_missing=False):
    """
    Validate all commits have matching PRs before proceeding with changelog generation.
    Returns True if validation passes or user chooses to continue, False to abort.
    """
    missing_matches = []

    logger.info("Validating all commits have matching PRs...")
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
            logger.warning(f"No matching PR found for commit {commit[:7]}: '{subject}'")

    if missing_matches:
        logger.warning(f"\n⚠️  Found {len(missing_matches)} commits without matching PRs:")
        for commit, subject in missing_matches:
            logger.warning(f"  - {commit[:10]}: {subject}")

        if not continue_on_missing:
            response = input("\nDo you want to continue anyway? (y/N): ").strip().lower()
            if response != 'y':
                logger.info("Aborting changelog generation. Please fix missing PR links.")
                return False

    return True


def main():
    parser = argparse.ArgumentParser(description='Advanced changelog generator')
    parser.add_argument('--from', dest='gfrom', help='Tag or commit to start from (exclusive)')
    parser.add_argument('--to', dest='gto', default='HEAD', help='Tag or commit to end at (inclusive)')
    parser.add_argument('--dry-run', action='store_true', help='Do not update PRs, just print what would be updated')
    parser.add_argument('--repo', default='weka/weka-operator', help='GitHub repository name (owner/repo)')
    parser.add_argument('--force', action='store_true', help='Continue even if some commits have no matching PRs')
    parser.add_argument('--review', action='store_true', help='Include PR links in the output for review purposes')
    parser.add_argument('--abort-on-miss', action='store_true', help='Abort if any SHA is missing from final output')
    parser.add_argument('--verbose', '-v', action='store_true', help='Enable verbose logging')
    args = parser.parse_args()

    # Configure logging
    log_level = logging.DEBUG if args.verbose else logging.INFO
    logging.basicConfig(
        level=log_level,
        format='%(asctime)s - %(levelname)s - %(message)s',
        handlers=[
            logging.FileHandler("changelog_generator.log"),
            logging.StreamHandler(sys.stderr)
        ]
    )

    gfrom = args.gfrom or get_latest_tag()
    gto = args.gto
    if not gfrom:
        logger.error('No --from specified and no tags found in repo.')
        sys.exit(1)

    commits = get_commit_range(gfrom, gto)
    if not commits:
        logger.error(f'No commits found in range {gfrom}..{gto}')
        sys.exit(0)

    # Initialize GitHub Client
    try:
        # REPO is now args.repo
        github_client = GitHubClient(repo_full_name=args.repo)
    except ValueError as e:
        logger.error(f"Failed to initialize GitHub client: {e}")
        sys.exit(1)

    # Fetch recent PRs once using the client
    num_commits = len(commits)
    # Fetch slightly more PRs than commits, adjust multiplier as needed
    num_prs_to_fetch = math.ceil(num_commits * 5) + 30
    logger.info(f"Attempting to fetch up to {num_prs_to_fetch} recent closed PRs for repo {args.repo}...")
    try:
        recent_prs = github_client.fetch_recent_closed_prs(num_prs_to_fetch)
        # Logging is handled within the client method
    except GitHubError as e:
        logger.error(f"Failed to fetch recent PRs: {e}")
        # Decide whether to exit or continue without PR matching
        sys.exit(1)  # Exit for now, as PR matching is crucial

    # Validate all commits have matching PRs before proceeding
    if not validate_commits_against_prs(commits, recent_prs, continue_on_missing=args.force):
        sys.exit(1)

    commit_infos = []
    processed_commit_count = 0
    repo_name = args.repo  # Pass repo name for potential use in process_commit
    for commit in commits:
        processed_commit_count += 1
        logger.info(f"Processing commit {processed_commit_count}/{num_commits}: {commit[:7]}...")
        try:
            ci = process_commit(commit, recent_prs, github_client, repo_name, dry_run=args.dry_run)
            # process_commit now handles the case where no matching PR is found
            if ci:
                commit_infos.append(ci)
        except GitHubError as e:
            logger.error(f"GitHub error processing commit {commit[:7]}: {e}. Skipping commit.")
            # Optionally add placeholder or mark as failed
        except Exception as e:
            logger.error(f"Unexpected error processing commit {commit[:7]}: {e}. Skipping commit.")
        # Removed early exit if PR not found, process_commit handles it

    logger.info("Aggregating release notes...")
    final_output = aggregate_release_notes(commit_infos, review_mode=args.review, abort_on_miss=args.abort_on_miss)
    # Only the final output goes to stdout
    print(final_output)


if __name__ == '__main__':
    main()
