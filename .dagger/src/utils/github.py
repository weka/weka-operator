import os
import requests
import logging
import json
import math
import sys
from typing import Optional, List, Dict, Any
from dataclasses import dataclass

# Set up logging for this module
logger = logging.getLogger(__name__)
# Add a default handler if no configuration is set by the calling script
if not logger.hasHandlers():
    handler = logging.StreamHandler(sys.stderr)
    formatter = logging.Formatter('%(asctime)s - %(name)s - %(levelname)s - %(message)s')
    handler.setFormatter(formatter)
    logger.addHandler(handler)
    # Set a default level (e.g., INFO) if needed, or rely on the caller's setup
    # logger.setLevel(logging.INFO)


GITHUB_API_URL = "https://api.github.com"


@dataclass
class PullRequestData:
    """Represents essential data for a GitHub Pull Request."""
    number: int
    title: str
    body: Optional[str]
    state: str
    html_url: str
    # url: str # API URL, less commonly needed directly by callers


class GitHubError(Exception):
    """Custom exception for GitHub API errors."""
    pass

class GitHubClient:
    """
    A client for interacting with the GitHub API, specifically for Pull Requests.
    """
    def __init__(self, repo_full_name: str, token: Optional[str] = None):
        """
        Initializes the GitHub client.

        Args:
            repo_full_name: The full name of the repository (e.g., 'owner/repo').
            token: GitHub Personal Access Token. If None, attempts to read from
                   the GITHUB_PAT_TOKEN environment variable.

        Raises:
            ValueError: If the token is not provided and cannot be found in the environment.
        """
        self.repo_full_name = repo_full_name
        self.token = token or os.environ.get("GITHUB_PAT_TOKEN")
        if not self.token:
            raise ValueError("GitHub token not provided and GITHUB_PAT_TOKEN environment variable is not set.")
        self.headers = {
            "Accept": "application/vnd.github.v3+json",
            "Authorization": f"Bearer {self.token}",
        }
        logger.info(f"GitHubClient initialized for repository: {self.repo_full_name}")

    def _request(self, method: str, endpoint: str, timeout: int = 20, **kwargs) -> Optional[Any]:
        """
        Internal helper to make authenticated GitHub API requests.

        Args:
            method: HTTP method (e.g., 'GET', 'POST', 'PATCH').
            endpoint: API endpoint path (e.g., '/repos/owner/repo/pulls').
            timeout: Request timeout in seconds.
            **kwargs: Additional arguments passed to requests.request.

        Returns:
            The JSON response payload, or None for 204 No Content responses.

        Raises:
            GitHubError: If the API request fails.
        """
        url = f"{GITHUB_API_URL}{endpoint}"
        logger.debug(f"Making GitHub API request: {method} {url}")
        try:
            response = requests.request(method, url, headers=self.headers, timeout=timeout, **kwargs)
            response.raise_for_status() # Raise HTTPError for bad responses (4xx or 5xx)
            if response.status_code == 204: # No Content
                logger.debug(f"Received 204 No Content for {method} {url}")
                return None
            logger.debug(f"Received {response.status_code} for {method} {url}")
            return response.json()
        except requests.exceptions.RequestException as e:
            logger.error(f"GitHub API request failed: {method} {url}")
            if hasattr(e, 'response') and e.response is not None:
                logger.error(f"Response status: {e.response.status_code}")
                # Avoid logging potentially sensitive response bodies unless debugging
                # logger.error(f"Response body: {e.response.text}")
            # Wrap the original exception
            raise GitHubError(f"GitHub API request failed for {method} {url}: {e}") from e

    def _request_raw(self, method: str, endpoint: str, timeout: int = 20, headers: Optional[Dict[str, str]] = None, **kwargs) -> Optional[str]:
        """
        Internal helper to make authenticated GitHub API requests expecting raw text response.

        Args:
            method: HTTP method (e.g., 'GET').
            endpoint: API endpoint path.
            timeout: Request timeout in seconds.
            headers: Optional custom headers to merge with default headers.
            **kwargs: Additional arguments passed to requests.request.

        Returns:
            The raw text response payload, or None if the request fails or returns no content.

        Raises:
            GitHubError: If the API request fails status check.
        """
        url = f"{GITHUB_API_URL}{endpoint}"
        request_headers = self.headers.copy()
        if headers:
            request_headers.update(headers)

        logger.debug(f"Making GitHub API raw request: {method} {url} with headers {request_headers}")
        try:
            response = requests.request(method, url, headers=request_headers, timeout=timeout, **kwargs)
            response.raise_for_status() # Raise HTTPError for bad responses (4xx or 5xx)
            if response.status_code == 204: # No Content
                logger.debug(f"Received 204 No Content for {method} {url}")
                return None
            logger.debug(f"Received {response.status_code} for {method} {url}, content length {len(response.text)}")
            return response.text
        except requests.exceptions.RequestException as e:
            logger.error(f"GitHub API raw request failed: {method} {url}")
            if hasattr(e, 'response') and e.response is not None:
                logger.error(f"Response status: {e.response.status_code}")
            # Wrap the original exception
            raise GitHubError(f"GitHub API raw request failed for {method} {url}: {e}") from e


    def fetch_recent_closed_prs(self, count: int) -> List[PullRequestData]:
        """
        Fetch the specified number of most recently updated closed PRs.

        Args:
            count: The maximum number of PRs to fetch.

        Returns:
            A list of PullRequestData objects.

        Raises:
            GitHubError: If the API request fails during pagination.
        """
        logger.info(f"Fetching up to {count} recent closed PRs for {self.repo_full_name}...")
        prs: List[PullRequestData] = []
        per_page = 100 # Max allowed by GitHub
        pages = math.ceil(count / per_page)
        remaining = count

        for page in range(1, pages + 1):
            page_size = min(remaining, per_page)
            endpoint = f"/repos/{self.repo_full_name}/pulls?state=closed&sort=updated&direction=desc&per_page={page_size}&page={page}"
            try:
                logger.debug(f"Fetching PRs page {page}/{pages} (size {page_size})")
                page_prs = self._request("GET", endpoint)
                if page_prs is None or not page_prs: # Stop if GitHub returns an empty list or None
                    logger.debug(f"No more PRs found on page {page}.")
                    break
                # Convert dicts to PullRequestData objects
                prs.extend([
                    PullRequestData(
                        number=pr_dict.get("number"),
                        title=pr_dict.get("title", ""),
                        body=pr_dict.get("body"), # Can be None
                        state=pr_dict.get("state", ""),
                        html_url=pr_dict.get("html_url", "")
                        # url=pr_dict.get("url", "")
                    )
                    for pr_dict in page_prs if pr_dict.get("number") is not None # Ensure number exists
                ])
                fetched_count = len(page_prs) # Count how many were actually returned by API
                remaining -= fetched_count
                logger.debug(f"Fetched {fetched_count} PRs from page {page}. {remaining} remaining to fetch.")
                if remaining <= 0:
                    break
            except GitHubError as e:
                # Log the error but re-raise to let the caller handle it
                logger.error(f"Error fetching PRs (page {page}): {e}")
                raise
        logger.info(f"Fetched a total of {len(prs)} PRs.")
        # Optional: Debug log fetched PR titles
        # for pr in prs:
        #     logger.debug(f"  - PR #{pr.number}: {pr.title}")
        return prs

    def get_pr_diff(self, pr_number: int) -> Optional[str]:
        """
        Fetches the diff content for a specific PR.

        Args:
            pr_number: The number of the pull request.

        Returns:
            A string containing the raw diff, or None if fetching fails.
        """
        endpoint = f"/repos/{self.repo_full_name}/pulls/{pr_number}"
        diff_headers = {"Accept": "application/vnd.github.v3.diff"}
        logger.debug(f"[{pr_number}] Fetching PR diff via GET {endpoint}")
        # Use the raw request helper
        return self._request_raw("GET", endpoint, headers=diff_headers)

    def get_pr_details(self, pr_number: int) -> Optional[PullRequestData]:
        """
        Fetches full details for a specific PR.

        Args:
            pr_number: The number of the pull request.

        Returns:
            A PullRequestData object containing the PR details, or None if fetching fails.
        """
        endpoint = f"/repos/{self.repo_full_name}/pulls/{pr_number}"
        logger.debug(f"[{pr_number}] Fetching PR details via GET {endpoint}")
        try:
            pr_dict = self._request("GET", endpoint)
            if pr_dict and pr_dict.get("number") is not None:
                return PullRequestData(
                    number=pr_dict.get("number"),
                    title=pr_dict.get("title", ""),
                    body=pr_dict.get("body"), # Can be None
                    state=pr_dict.get("state", ""),
                    html_url=pr_dict.get("html_url", "")
                    # url=pr_dict.get("url", "")
                )
            return None # Return None if request failed or essential data missing
        except GitHubError as e:
            # Log the error but return None as per previous behavior
            logger.error(f"Error fetching PR details for #{pr_number}: {e}")
            return None

    def fetch_pr_body(self, pr_number: int) -> Optional[str]:
        """
        Fetches just the body of a specific PR.

        Args:
            pr_number: The number of the pull request.

        Returns:
            The PR body string, or None if fetching fails or the PR has no body.
        """
        logger.debug(f"[{pr_number}] Fetching PR body.")
        details = self.get_pr_details(pr_number)
        if details:
            return details.body # Return body (can be None) or empty string if needed by caller
        return None # Return None if get_pr_details failed

    def update_pr_body(self, pr_number: int, new_body: str) -> Optional[PullRequestData]:
        """
        Updates the PR body using the GitHub API.

        Args:
            pr_number: The number of the pull request.
            new_body: The new content for the PR body.

        Returns:
            A PullRequestData object representing the updated PR, or None if update fails.

        Raises:
            GitHubError: If the API request fails.
        """
        endpoint = f"/repos/{self.repo_full_name}/pulls/{pr_number}"
        payload = json.dumps({"body": new_body})
        logger.info(f"[{pr_number}] Updating PR body via PATCH {endpoint}")
        try:
            # Use a slightly shorter timeout for PATCH as it might hang otherwise?
            pr_dict = self._request("PATCH", endpoint, data=payload, timeout=15)
            logger.info(f"Successfully updated PR #{pr_number}")
            if pr_dict and pr_dict.get("number") is not None:
                return PullRequestData(
                    number=pr_dict.get("number"),
                    title=pr_dict.get("title", ""),
                    body=pr_dict.get("body"), # Can be None
                    state=pr_dict.get("state", ""),
                    html_url=pr_dict.get("html_url", "")
                    # url=pr_dict.get("url", "")
                )
            return None # Return None if update failed or essential data missing
        except GitHubError as e:
            # Log the error but re-raise to ensure the caller knows the update failed
            logger.error(f"Failed to update PR #{pr_number}: {e}")
            raise
