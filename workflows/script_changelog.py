#!/usr/bin/env -S uv run --no-project --with openai-agents
import argparse
import asyncio
# Removed requests import, handled by GitHubClient
import logging
import math  # Added for ceiling division
import os
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


# --- Submodule (weka-k8s-api) Support ---
SUBMODULE_PATH = "pkg/weka-k8s-api"


def get_submodule_hash_change(commit: str) -> tuple[str, str] | None:
    """Detect if a commit updates the weka-k8s-api submodule, return (old_hash, new_hash)."""
    result = subprocess.run(
        ['git', 'diff-tree', '--no-commit-id', '-r', commit, '--', SUBMODULE_PATH],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE
    )
    if result.returncode != 0 or not result.stdout:
        return None

    # Format: :160000 160000 old_hash new_hash M pkg/weka-k8s-api
    line = result.stdout.decode().strip()
    if not line:
        return None

    parts = line.split()
    if len(parts) >= 4:
        return (parts[2], parts[3])
    return None


def get_submodule_commits(old_hash: str, new_hash: str) -> list[str]:
    """Get list of commit SHAs in the API submodule between old and new hashes."""
    # Ensure submodule is initialized and has the commits
    if not os.path.exists(os.path.join(SUBMODULE_PATH, '.git')):
        logger.warning(f"Submodule at {SUBMODULE_PATH} not initialized")
        return []

    # Fetch to ensure we have the commits
    subprocess.run(['git', '-C', SUBMODULE_PATH, 'fetch', '--all'],
                   stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    result = subprocess.run(
        ['git', '-C', SUBMODULE_PATH, 'rev-list', '--reverse', f'{old_hash}..{new_hash}'],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE
    )
    if result.returncode != 0:
        logger.error(f"Failed to get submodule commits: {result.stderr.decode()}")
        return []
    return result.stdout.decode().strip().splitlines()


def get_submodule_commit_message(commit: str) -> str:
    """Get commit message from API submodule."""
    result = subprocess.run(
        ['git', '-C', SUBMODULE_PATH, 'log', '--format=%B', '-n', '1', commit],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE
    )
    return result.stdout.decode().strip() if result.returncode == 0 else ""


def get_submodule_commit_show(commit: str) -> str:
    """Get full commit show output from API submodule."""
    result = subprocess.run(
        ['git', '-C', SUBMODULE_PATH, 'show', '--format=fuller', commit],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE
    )
    return result.stdout.decode() if result.returncode == 0 else ""


def process_api_commit(commit: str, dry_run: bool = False) -> CommitInfo | None:
    """Process a single commit from the weka-k8s-api submodule."""
    message = get_submodule_commit_message(commit)
    if not message:
        logger.warning(f"API commit {commit[:7]}: Could not get message")
        return None

    subject = message.splitlines()[0]
    ctype = infer_type(message)

    # Skip non-user-facing commits
    if ctype not in ('fix', 'feature', 'breaking'):
        logger.info(f"API commit {commit[:7]} type '{ctype}' not fix/feature/breaking. Ignoring.")
        return None

    # Use prefixed SHA to distinguish API commits
    commit_info = CommitInfo(sha=f"api:{commit}", subject=f"[API] {subject}", ctype=ctype)

    # Get diff for generation
    diff_content = get_submodule_commit_show(commit)
    if len(diff_content.encode('utf-8')) > MAX_DIFF_SIZE:
        diff_content = diff_content[:MAX_DIFF_SIZE] + "\n...(truncated)"

    # Generate release notes using the same function
    rn = generate_release_notes(
        title=f"[API] {subject}",
        ctype=ctype,
        original_body=message,
        diff_or_show_content=diff_content
    )

    commit_info.release_notes = rn
    if rn == "Ignored":
        commit_info.ignored = True
        logger.info(f"API commit {commit[:7]}: Generated 'Ignored'")
    else:
        logger.info(f"API commit {commit[:7]}: Generated release notes")

    return commit_info


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

def process_commit(commit, recent_prs, github_client: GitHubClient, repo_name: str, dry_run=False) -> tuple[CommitInfo | None, list[CommitInfo]]:
    """Process a commit and return both operator CommitInfo and any API CommitInfos from submodule changes."""
    api_commit_infos = []

    # Check for submodule update and process API commits
    submodule_change = get_submodule_hash_change(commit)
    if submodule_change:
        old_hash, new_hash = submodule_change
        logger.info(f"Commit {commit[:7]}: Detected API submodule update {old_hash[:7]}..{new_hash[:7]}")

        api_commits = get_submodule_commits(old_hash, new_hash)
        logger.info(f"Found {len(api_commits)} commits in API submodule")

        for api_commit in api_commits:
            api_ci = process_api_commit(api_commit, dry_run)
            if api_ci and not api_ci.ignored:
                api_commit_infos.append(api_ci)

    message = get_commit_message(commit)
    subject = message.splitlines()[0]
    # Use imported infer_type
    ctype = infer_type(message)

    # Immediately ignore commits that are not fix, feature, or breaking
    if ctype not in ('fix', 'feature', 'breaking'):
        logger.info(f"Commit {commit[:7]} type '{ctype}' is not fix/feature/breaking. Marking as ignored.")
        # Create CommitInfo marked as ignored and return with any API commits
        return (CommitInfo(sha=commit, subject=subject, ctype=ctype, ignored=True, release_notes="Ignored (type)"), api_commit_infos)

    # If type is valid, proceed with creating CommitInfo and finding PR
    commit_info = CommitInfo(sha=commit, subject=subject, ctype=ctype)

    # Find PR by matching title with commit subject (using PullRequestData)
    matched_pr = None
    for pr in recent_prs:
        if pr.title == subject:
            matched_pr = pr
            break

    if matched_pr:
        pr_number = matched_pr.number
        # Body is directly accessible from the dataclass
        pr_body = matched_pr.body or "" # Use attribute access, fallback for None

        commit_info.pr_body = pr_body
        commit_info.pr_number = pr_number
        # Optionally store html_url if needed for links later
        # commit_info.pr_html_url = matched_pr.html_url
        rn_tag = extract_release_notes_tag(pr_body)

        if rn_tag is not None:
            if rn_tag.lower() == "ignored":
                commit_info.release_notes = "Ignored"
                commit_info.ignored = True
                logger.info(f"Commit {commit[:7]}: Found existing 'Ignored' tag in PR #{pr_number}. Skipping.")
                return (commit_info, api_commit_infos)  # Already processed and ignored
            else:
                commit_info.release_notes = rn_tag
                logger.info(f"Commit {commit[:7]}: Found existing release notes tag in PR #{pr_number}.")
                return (commit_info, api_commit_infos)  # Use existing notes

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

        return (commit_info, api_commit_infos)

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

    return (commit_info, api_commit_infos)


def aggregate_release_notes(commit_infos: List[CommitInfo], review_mode=False, abort_on_miss=False):
    # Only include non-ignored
    included = [ci for ci in commit_infos if ci and not ci.ignored]
    if not included:
        return "No user-facing changes found in this range."

    # Repo name for PR links - hardcoded for now
    # TODO: Pass repo_name explicitly or get from client if possible
    repo_name_for_links = "weka/weka-operator"

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
        Some commits may be prefixed with [API] indicating they are from the weka-k8s-api submodule (CRD/API type changes).

        **Important for API changes:**
        - API changes typically involve new CRD fields, validations, or type changes
        - Group related API changes with their corresponding operator changes when applicable
        - Clearly indicate when API/CRD changes require user action or affect configuration
        - API commit SHAs are prefixed with "api:" (e.g., api:abc1234)

        Combine and structure them into a user-facing release notes.
        You may merge similar/related commits into a single entry, but preserve all commits SHAs.
        You may drop items if they are not user-facing, with high level of confidence.
        Output in markdown. Each change must be clearly referenced by commit SHA.
        Consolidate Reverts/Disable of features into a single entry, but keep references. Rely on PR number to establish if revert was done before or after feature was introduced. If there are newer(by PR number) PRs that mention the feature after it was reverted/disabled - assume that feature was re-enabled. If conclusion is that feature is disabled at final state - mention all relevant PRs as dropped
        <instructions>
        - Do not wrap pull requests as markdown links, just put links as-is, as a space separated list. Make sure to include all PRs in appropriate place
        - Make sure to include all input items in the output, do not drop any of them, they are separated by <item> tags in the input
        - IF for whatever reason something was dropped - list this items explicitly, in the end of the output, explaining why
        - [Group name] is a category name, it can be "Breaking changes", "Features", "Improvements", "Fixed Issues" in this order
            - If there is no content for the group do not include it  
            - Improvements should be extracted from "Fixes" category, if it's not a bugfix but some new minor functionality marked as fix
        <instructions>
        <trademarks_instructions>
        - For any occurrence of stand-alone Weka word - replace it with WEKA, uppercase
            - if something like "wekafsio" appears, or "wekafs" - keep as is, replace only stand-alone WEKA word
        - Any occurrence of "AWS EKS" should be replaced with "Amazon EKS"
        </trademarks_instructions>
        <additional_styling_instructions>
        You are a professional technical writer. You are the precision architect behind high-performance documentation.
As the lead technical writer in the data platforms space, you thrive on refining content to perfection—whether it’s CLI references, task procedures, or release notes. Every output is sharp, user-aligned, and engineered for clarity at scale.
Your content doesn’t just inform; it accelerates adoption, reduces friction, and speaks the language of both systems and stakeholders. It’s not just documentation—it’s enablement with edge.
Your mission statement is to enable employees, partners, and customers to work effectively with the WEKA solutions by providing goal-oriented, correct, concise, and clear product information.
To support the documentation mission statement, use the topic-based writing (TBW) methodology. In short, TBW supports the following goals:
- Goal-oriented: Users do not want to read product information; they want to complete a task or achieve a goal. TBW eliminates the need for users to read extensively about product features or UI navigation to find the required information.
- Stand-alone topics: The TBW approach breaks information into self-contained units (topics). Each topic has a specific purpose. Each
topic is usable by itself. And although a topic can be relevant to or used for
another topic, it is complete on its own.
- Clear and descriptive titles: A clear and descriptive title helps users locate meaningful content. Labels within the topic, such as subtopics and example titles, enable the user to focus on specific information.
- Consistent structure and style: Similar content presented in a consistent structure using consistent terms helps users: (a) Find the information that they need quickly. (b) Locate related information. (c) Focus on content rather than form.
- Findability: Dynamic links or references connect topics to related subjects or supporting information.
- Minimalist writing: Minimalist writing focuses on providing only the information users need and nothing more. Minimalist writing is a natural companion to topic-based writing, focusing on user needs and information relevance.
Topic types
1. Task topics: Task topics include specific elements that structure the procedural information. It answers the three WWH questions: What is this topic? Why do I need to use it, or when? How to perform the procedure (numbered steps).
2. Concept topics: Concept topics provide information that users need to understand before they use a product or start a task. They support the “learning” type of information-seeking behavior.
3. ​Reference topics: Provide a quick and easy access to important information needed to perform specific tasks or answer questions. The information is typically presented in a structured table, list, or diagram
format.
TBW benefits
Writing self-contained topics focused on user goals is beneficial for both users and writers.
Benefits for readers
Readers quickly recognize the structural patterns you have created and then use those cues to locate what they need.
In general, users can:
- Find the topic they need faster
- Read only the information they need to read—Skip, Scan, and Retrieve!
- Have clearer, more concise, and more accurate content
- Find critical content without redundant information
- Experience consistent information structure
- Accomplish their tasks efficiently
Benefits for the writer
The guidelines for topics and topic types give writers structure and direction, while user profiles and task analysis dictate what content to include.
In general, writers can:
- Design new information more efficiently and consistently
- Eliminate unimportant and redundant information
- Organize or reorganize content quickly and easily
- Stay focused on the needs of the user and the purpose of the information
- Maintain consistency regardless of the number of writers on a document
- Identify missing data and inconsistencies
- Create content that can be reused as required
Do not include in your response mentions like reference, concept, what, why, or how. The document should be fluent.
In addition, adhere to the following:
Avoid passive voice; use active voice when possible.
Use present simple sentences; Avoid past or future tenses unless it is required to address a certain idea.
Use sentence caps in titles and headings
Avoid Latin terms such as e.g., i.e., etc. via; Use alternative English terms.
Do not begin with “This page...” or “This topic ...” for the line under topic titles. Use a verb like explore, learn, and so on.
    </additional_styling_instructions>
        """

    instructions += """Output in following format:
        <format>
        # [Group name]
        ### Title
        [Description]
        Commits: [Commit SHA], [Commit SHA], ...
        PRs: PR_LINK, PR_LINK, ... //if any, skip PRs if not in review mode/no PRs available
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

    # Validate commits (SHAs) - handles both regular SHAs and API SHAs (api:abc1234)
    sha_pattern = r"(?:api:)?[0-9a-f]{7,40}"  # SHA-1 hashes, optionally prefixed with "api:"
    found_shas = set(sha[:11] if sha.startswith("api:") else sha[:7] for sha in re.findall(sha_pattern, final_output))
    expected_shas = set(ci.sha[:11] if ci.sha.startswith("api:") else ci.sha[:7] for ci in included)
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
        # Find matching PR (using PullRequestData)
        matched = False
        for pr in recent_prs:
            if pr.title == subject:
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
    num_prs_to_fetch = math.ceil(num_commits * 3) + 30
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
    api_commit_infos = []  # Collect API changes from submodule updates
    processed_commit_count = 0
    repo_name = args.repo  # Pass repo name for potential use in process_commit
    for commit in commits:
        processed_commit_count += 1
        logger.info(f"Processing commit {processed_commit_count}/{num_commits}: {commit[:7]}...")
        try:
            ci, api_cis = process_commit(commit, recent_prs, github_client, repo_name, dry_run=args.dry_run)
            # process_commit now handles the case where no matching PR is found
            if ci:
                commit_infos.append(ci)
            api_commit_infos.extend(api_cis)  # Collect API changes
        except GitHubError as e:
            logger.error(f"GitHub error processing commit {commit[:7]}: {e}. Skipping commit.")
            # Optionally add placeholder or mark as failed
        except Exception as e:
            logger.error(f"Unexpected error processing commit {commit[:7]}: {e}. Skipping commit.")
        # Removed early exit if PR not found, process_commit handles it

    # Combine operator and API changes for aggregation
    all_commit_infos = commit_infos + api_commit_infos
    logger.info(f"Aggregating release notes ({len(commit_infos)} operator, {len(api_commit_infos)} API)...")
    final_output = aggregate_release_notes(all_commit_infos, review_mode=args.review, abort_on_miss=args.abort_on_miss)
    # Only the final output goes to stdout
    print(final_output)


if __name__ == '__main__':
    main()
