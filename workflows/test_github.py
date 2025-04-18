import os
import re
import subprocess

import pytest

from github import GitHubClient


def get_repo_full_name():
    """
    Retrieves the GitHub repository full name (owner/repo) from the git remote origin URL.
    """
    try:
        url = subprocess.check_output(
            ['git', 'config', '--get', 'remote.origin.url'],
            encoding='utf-8'
        ).strip()
    except subprocess.CalledProcessError as e:
        pytest.skip(f"Could not get remote origin URL: {e}")

    # Match URLs like git@github.com:owner/repo.git or https://github.com/owner/repo.git
    match = re.match(
        r"(?:git@github.com:|https://github.com/)([^/]+/[^/]+)(?:\.git)?$",
        url
    )
    if not match:
        pytest.skip(f"Unsupported remote URL format: {url}")
    return match.group(1)


def test_get_pr_diff_integration():
    """
    Integration test for GitHubClient.get_pr_diff using a real GitHub API call.
    Requires:
      - GITHUB_PAT_TOKEN environment variable set to a valid GitHub token with repo access.
      - The git remote 'origin' to point to a GitHub repository containing PR #1084.
    """
    token = os.environ.get('GITHUB_PAT_TOKEN')
    if not token:
        pytest.skip("GITHUB_PAT_TOKEN environment variable not set")

    repo_full_name = get_repo_full_name()
    client = GitHubClient(repo_full_name=repo_full_name, token=token)
    pr_number = 1084
    diff = client.get_pr_diff(pr_number)
    # Ensure we got some diff content
    assert diff is not None, f"Expected diff for PR #{pr_number}, got None"
    # Basic sanity check: diff header present
    assert 'diff --git' in diff, f"Diff for PR #{pr_number} does not contain expected header"