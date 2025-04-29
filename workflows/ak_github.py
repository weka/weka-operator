import time
import os
import json
import re # Import re for body manipulation

from autokitteh import activity

# Removed requests import
from github import GitHubClient, GitHubError, PullRequestData # Import the new client and dataclass
from release_notes_utils import infer_type, generate_release_notes # Import shared utils

# --- Constants ---
STABILIZATION_WAIT_SECONDS = 600  # 10 minutes - TODO: Make configurable?
POLLING_INTERVAL_SECONDS = 30 # Interval to check PR status

def process_pr_event(raw_event) -> None:
    """
    Main entry point to process a GitHub pull request event (opened, edited, etc.).
    Parses the raw event, fetches PR details, and manages the state machine
    for waiting for body population and stabilization before updating the PR body.
    """
    print("Received raw event.")
    # Extract the JSON payload string
    payload_str = raw_event.data.body.form.payload
    if not payload_str:
        print("Error: Could not find 'payload' in raw_event.body.form")
        return

    try:
        event_data = json.loads(payload_str)
        # print("Parsed event payload:", json.dumps(event_data, indent=2)) # Debug: Print parsed payload
    except json.JSONDecodeError as e:
        print(f"Error: Failed to parse JSON payload: {e}")
        return

    # Extract key information
    action = event_data.get("action")
    pr_data = event_data.get("pull_request", {})
    repo_data = event_data.get("repository", {})

    pr_number = event_data.get("number") # Present for most PR events
    if not pr_number and pr_data:
         pr_number = pr_data.get("number") # Fallback for some event types

    repo_full_name = repo_data.get("full_name")

    if not action or not pr_number or not repo_full_name:
        print("Error: Missing essential data (action, number, repository.full_name) in event payload.")
        print(f"Action: {action}, PR Number: {pr_number}, Repo: {repo_full_name}")
        return

    print(f"Processing Action '{action}' for PR #{pr_number} in repo '{repo_full_name}'.")
    # PR URL will be available after fetching details if needed

    # We only care about 'opened' and 'edited' as initial triggers for our logic.
    # 'closed' events will be handled during polling loops.
    # Other actions like 'synchronize', 'review_requested' etc. are ignored by this specific workflow.
    if action not in ["opened"]:
        print(f"Ignoring action '{action}' as it's not 'opened'.")
        return

    # Initialize GitHub Client
    try:
        github_client = GitHubClient(repo_full_name=repo_full_name)
    except ValueError as e:
        # This happens if GITHUB_PAT_TOKEN is not set
        print(f"Error initializing GitHub client: {e}")
        return # Cannot proceed without a token

    try:
        # Get initial PR state using the client
        current_pr = github_client.get_pr_details(pr_number)
        if not current_pr:
             # Error logged within get_pr_details
             print(f"[{pr_number}] Failed to fetch initial PR details. Aborting.")
             return

        print(f"[{pr_number}] Initial PR URL: {current_pr.html_url}") # Log URL now

        if current_pr.state == "closed":
            print(f"[{pr_number}] PR is already closed. No action needed.")
            return

        # Check initial body state
        initial_body = current_pr.body or "" # Use attribute access
        if initial_body.strip(): # Check if non-empty after stripping whitespace
            print(f"[{pr_number}] PR already has a body. Proceeding to stabilization wait.")
            # Pass the client instance to the waiting functions
            _wait_for_stabilization(repo_full_name, pr_number)
        else:
            print(f"[{pr_number}] PR body is initially empty. Waiting for body content.")
            # Pass the client instance to the waiting functions
            _wait_for_body(repo_full_name, pr_number)

    except GitHubError as e:
        # Catch specific GitHub errors from the client
        print(f"A GitHub API error occurred during processing PR #{pr_number}: {e}")
    except Exception as e:
        print(f"An unexpected error occurred during processing PR #{pr_number}: {e}")
        # No explicit cleanup needed here as there's no subscription
        # Consider whether to raise or just log and exit depending on autokitteh's error handling
        # raise # Re-raise the exception for now


# --- Helper Functions ---

# Functions now accept GitHubClient instance
@activity
def _wait_for_body(repo_full_name:str, pr_number: int) -> None:
    github_client = GitHubClient(repo_full_name=repo_full_name)
    """
    Polls the PR until its body is populated or it is closed.
    """
    print(f"[{pr_number}] Waiting for body to be populated...")
    while True:
        time.sleep(POLLING_INTERVAL_SECONDS)
        try:
            # Use the client to get details
            current_pr = github_client.get_pr_details(pr_number)
            if not current_pr:
                # Error logged within get_pr_details
                print(f"[{pr_number}] Failed to fetch PR details while waiting for body. Retrying...")
                continue

            state = current_pr.state
            body = current_pr.body or "" # Use attribute access

            if state == "closed":
                print(f"[{pr_number}] PR closed while waiting for body.")
                # Pass the client instance and the last known state (PullRequestData object)
                _handle_premature_close(github_client, pr_number, current_pr)
                break
            elif body.strip(): # Check if non-empty after stripping
                print(f"[{pr_number}] Body populated. Proceeding to stabilization.")
                # Pass the client instance
                _wait_for_stabilization(repo_full_name, pr_number)
                break
            else:
                print(f"[{pr_number}] Body still empty. Continuing to wait.")

        except GitHubError as e:
             print(f"[{pr_number}] GitHub API error during wait_for_body polling: {e}. Retrying...")
        except Exception as e:
            print(f"[{pr_number}] Unexpected error during wait_for_body polling: {e}. Retrying...")
            # Add resilience, maybe break after too many errors?


# Update function signature
@activity
def _wait_for_stabilization(repo_full_name:str, pr_number: int) -> None:
    """
    Waits for a period of inactivity (no body changes) before processing.
    Polls the PR state. Restarts the wait if the body is edited. Handles PR closure.
    """
    github_client = GitHubClient(repo_full_name=repo_full_name)
    print(f"[{pr_number}] Entering stabilization wait ({STABILIZATION_WAIT_SECONDS}s)...")
    stabilization_start_time = time.time()

    try:
        # Use client
        initial_pr = github_client.get_pr_details(pr_number)
        if not initial_pr:
             # Error logged within get_pr_details
             print(f"[{pr_number}] Failed to get initial PR details for stabilization. Aborting.")
             return
        last_known_body = initial_pr.body or "" # Use attribute access
    except GitHubError as e:
        print(f"[{pr_number}] GitHub error getting initial PR details for stabilization: {e}. Aborting.")
        return
    except Exception as e:
        print(f"[{pr_number}] Unexpected error getting initial PR details for stabilization: {e}. Aborting.")
        return

    while True:
        time.sleep(POLLING_INTERVAL_SECONDS)
        try:
            # Use client
            current_pr = github_client.get_pr_details(pr_number)
            if not current_pr:
                # Error logged within get_pr_details
                print(f"[{pr_number}] Failed to fetch PR details during stabilization. Retrying...")
                continue # Skip this cycle

            current_state = current_pr.state
            current_body = current_pr.body or "" # Use attribute access

            if current_state == "closed":
                print(f"[{pr_number}] PR closed during stabilization wait.")
                # Pass client instance and PullRequestData object
                _handle_premature_close(github_client, pr_number, current_pr)
                break # Exit loop

            if current_body != last_known_body: # Compare potentially None/empty string correctly
                print(f"[{pr_number}] Body edited during stabilization. Resetting wait timer.")
                stabilization_start_time = time.time()
                last_known_body = current_body # Update last known body
                # Continue loop to wait full duration again
            else:
                # Body has not changed, check if timer expired
                elapsed_time = time.time() - stabilization_start_time
                if elapsed_time >= STABILIZATION_WAIT_SECONDS:
                    print(f"[{pr_number}] Stabilization period ended without body changes.")
                    # Pass client instance and PullRequestData object
                    _generate_and_update(github_client, pr_number, current_pr)
                    break # Exit loop
                else:
                    remaining = STABILIZATION_WAIT_SECONDS - elapsed_time
                    print(f"[{pr_number}] Body stable. {remaining:.0f}s remaining in stabilization window.")
                    # Continue loop

        except GitHubError as e:
             print(f"[{pr_number}] GitHub API error during stabilization polling: {e}. Retrying...")
        except Exception as e:
             print(f"[{pr_number}] Unexpected error during stabilization polling: {e}. Retrying...")
             # Add resilience


# Update function signature to accept PullRequestData
def _generate_and_update(github_client: GitHubClient, pr_number: int, current_pr_data: PullRequestData) -> None:
    """
    Fetches PR diff, generates release notes using shared utility, formats the body,
    and updates the pull request via API using PullRequestData.
    """
    pr_title = current_pr_data.title
    original_body = current_pr_data.body or "" # Use attribute access

    # 1. Check if release notes tag already exists and is not empty/ignored
    existing_rn_match = re.search(r"<release_notes>(.*?)</release_notes>", original_body, re.DOTALL)
    if existing_rn_match:
        existing_rn_content = existing_rn_match.group(1).strip()
        if existing_rn_content and existing_rn_content.lower() != "ignored":
            print(f"[{pr_number}] Found existing, non-empty release notes tag. Skipping generation and update.")
            print(f"[{pr_number}] Workflow finished for PR (existing notes found).")
            return # Don't overwrite existing notes unless they are empty or explicitly "Ignored"

    print(f"[{pr_number}] Generating release notes for PR: '{pr_title}'")

    # 2. Fetch PR diff
    print(f"[{pr_number}] Fetching PR diff...")
    try:
        diff_content = github_client.get_pr_diff(pr_number)
        if diff_content is None:
            print(f"[{pr_number}] Failed to fetch PR diff. Cannot generate release notes.")
            # Potentially update PR body with an error message? Or just log and exit.
            # For now, just log and exit this function.
            return
    except GitHubError as e:
        print(f"[{pr_number}] GitHub error fetching PR diff: {e}. Cannot generate release notes.")
        return
    except Exception as e:
        print(f"[{pr_number}] Unexpected error fetching PR diff: {e}. Cannot generate release notes.")
        return

    # 3. Infer type and Generate Release Notes
    ctype = infer_type(pr_title)
    print(f"[{pr_number}] Inferred type: {ctype}")
    # Pass original_body to the generation function
    rn = generate_release_notes(pr_title, ctype, original_body, diff_content)
    print(f"[{pr_number}] Generated release notes result: '{'Ignored' if rn == 'Ignored' else 'Generated'}'") # Avoid printing full notes in log

    # 4. Format the new body with <release_notes> tag
    tag_to_insert = f"<release_notes>\n{rn}\n</release_notes>"
    new_body_content = original_body # Start with the original

    if existing_rn_match: # If tag exists (meaning it was empty or "Ignored" based on check above)
        print(f"[{pr_number}] Replacing existing release notes tag.")
        new_body_content = re.sub(r"<release_notes>.*?</release_notes>", tag_to_insert, new_body_content, flags=re.DOTALL, count=1)
    else:
        print(f"[{pr_number}] Appending new release notes tag.")
        # Append with separating newlines if body is not empty
        separator = "\n\n" if new_body_content else ""
        new_body_content = new_body_content + separator + tag_to_insert

    # 5. Update PR Body via API
    if new_body_content.strip() == original_body.strip():
         print(f"[{pr_number}] No changes to PR body needed. Skipping update.")
    else:
        print(f"[{pr_number}] Updating PR body...")
        try:
            # Use client to update
            github_client.update_pr_body(pr_number, new_body_content)
            # Success/failure logging is handled within the client method
        except GitHubError as e:
            # Error is already logged by the client, just note failure here
            print(f"[{pr_number}] Failed to update PR body via client.")
            # Decide if you want to retry or just log and finish
        except Exception as e:
            # Catch unexpected errors during update
            print(f"[{pr_number}] Unexpected error updating PR body: {e}")

    print(f"[{pr_number}] Workflow finished for PR after generation/update attempt.")


# Update function signature to accept PullRequestData
def _handle_premature_close(github_client: GitHubClient, pr_number: int, closed_pr_data: PullRequestData) -> None:
    """
    Handles the case where the PR is closed before the main logic completes.
    Attempts to generate and update the body based on the last known state using PullRequestData.
    Fetching diff for a closed PR might fail.
    """
    pr_title = closed_pr_data.title
    original_body = closed_pr_data.body or "" # Use attribute access

    print(f"[{pr_number}] PR was closed prematurely. Attempting final generation based on last known state for: '{pr_title}'.")

    # Check if release notes already exist and are valid
    existing_rn_match = re.search(r"<release_notes>(.*?)</release_notes>", original_body, re.DOTALL)
    if existing_rn_match:
        existing_rn_content = existing_rn_match.group(1).strip()
        if existing_rn_content and existing_rn_content.lower() != "ignored":
            print(f"[{pr_number}] Found existing, non-empty release notes tag in closed PR. No update needed.")
            print(f"[{pr_number}] Workflow finished for prematurely closed PR (existing notes found).")
            return

    # Attempt to fetch diff (might fail for closed/merged PRs)
    diff_content = None
    print(f"[{pr_number}] Attempting to fetch diff for closed PR (may fail)...")
    try:
        diff_content = github_client.get_pr_diff(pr_number)
        if diff_content is None:
            print(f"[{pr_number}] Could not fetch diff for closed PR. Generation will rely on title only.")
            # Use a placeholder or empty string if diff is crucial for the agent
            diff_content = "(Could not fetch diff for closed PR)"
    except Exception as e:
        print(f"[{pr_number}] Error fetching diff for closed PR: {e}. Generation will rely on title only.")
        diff_content = f"(Error fetching diff: {e})"

    # Infer type and Generate Release Notes
    ctype = infer_type(pr_title)
    print(f"[{pr_number}] Inferred type (closed PR): {ctype}")
    # Pass original_body to the generation function
    rn = generate_release_notes(pr_title, ctype, original_body, diff_content)
    print(f"[{pr_number}] Generated release notes result (closed PR): '{'Ignored' if rn == 'Ignored' else 'Generated'}'")

    # Format the new body
    tag_to_insert = f"<release_notes>\n{rn}\n</release_notes>"
    new_body_content = original_body
    if existing_rn_match:
        print(f"[{pr_number}] Replacing existing tag in closed PR.")
        new_body_content = re.sub(r"<release_notes>.*?</release_notes>", tag_to_insert, new_body_content, flags=re.DOTALL, count=1)
    else:
        print(f"[{pr_number}] Appending new tag to closed PR.")
        separator = "\n\n" if new_body_content else ""
        new_body_content = new_body_content + separator + tag_to_insert

    # Attempt to update (might fail)
    if new_body_content.strip() == original_body.strip():
        print(f"[{pr_number}] No changes to closed PR body needed. Skipping update.")
    else:
        print(f"[{pr_number}] Attempting to update closed PR body (might fail)...")
        # Editing a closed PR might not be possible or desired depending on repo settings/permissions
        # Wrap in try-except to avoid session failure if update fails
        try:
            # Use client to update
            github_client.update_pr_body(pr_number, new_body_content)
            # Success/failure logging handled by client
        except GitHubError as edit_error:
            # Error logged by client, provide context here
            print(f"[{pr_number}] Could not update body of closed PR (expected if permissions restrict or already deleted/merged).")
        except Exception as edit_error:
            print(f"[{pr_number}] Unexpected error updating closed PR body: {edit_error}")

    print(f"[{pr_number}] Workflow finished for prematurely closed PR.")
