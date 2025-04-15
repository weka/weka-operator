#!/usr/bin/env python3

import os
import logging
from pathlib import Path
from typing import List, Dict

# Set up logging
logging = logging.getLogger(__name__)

# Define the functions locally to avoid dependencies
def is_path_allowed(file_path):
    # Convert to Path object and return it
    return Path(file_path)

def read_file(file_path: str) -> str:
    """
    Reads the content of a specified absolute file path.
    Access is restricted to pre-approved directories.

    Args:
        file_path: The absolute path of the file to read.

    Returns:
        The content of the file as a string, or an error message string
        if access is denied, the path is not a file, or an error occurs.
    """
    allowed_path = is_path_allowed(file_path)

    if not allowed_path:
        return f"Error: Access denied or invalid path: {file_path}"

    if not allowed_path.is_file():
        return f"Error: Path is not a file: {allowed_path}"

    try:
        content = Path(file_path).read_text(encoding='utf-8', errors='ignore')
        logging.info(f"Successfully read file: {allowed_path}")
        return content
    except PermissionError:
        logging.warning(f"Permission denied for reading file: {allowed_path}")
        return f"Error: Permission denied for file: {allowed_path}"
    except FileNotFoundError:
        logging.warning(f"File not found during read attempt: {allowed_path}")
        return f"Error: File not found: {allowed_path}"
    except Exception as e:
        logging.error(f"Error reading file {allowed_path}: {e}")
        return f"Error: Could not read file: {e}"

def read_multiple_files(file_paths: List[str]) -> Dict[str, str]:
    """
    Reads the contents of multiple specified absolute file paths.
    Access is restricted to pre-approved directories for each file.

    Args:
        file_paths: A list of absolute file paths to read.

    Returns:
        A dictionary where keys are the original file paths and values are
        either the file content (string) or an error message (string) for that specific file.
    """
    logging.info(f"Tool 'read_multiple_files' called with paths: {file_paths}")
    results: Dict[str, str] = {}
    for file_path in file_paths:
        # Use the existing read_file logic for each path
        content_or_error = read_file(file_path)
        results[file_path] = content_or_error
        # Logging is handled within read_file

    logging.info(f"Finished processing multiple files. Results count: {len(results)}")
    return results

def write_file(file_path: str, content: str) -> str:
    """
    Writes content to a specified absolute file path.
    If directories in the path do not exist, they will be created automatically.
    Access is restricted to pre-approved directories.

    Args:
        file_path: The absolute path of the file to write.
        content: The content to write to the file.

    Returns:
        A success message string, or an error message string if access is denied or an error occurs.
    """
    allowed_path = is_path_allowed(file_path)

    if not allowed_path:
        return f"Error: Access denied or invalid path: {file_path}"

    try:
        # Create parent directories if they don't exist
        parent_dir = Path(file_path).parent
        parent_dir.mkdir(parents=True, exist_ok=True)

        # Write content to file
        Path(file_path).write_text(content, encoding='utf-8')
        logging.info(f"Successfully wrote to file: {file_path}")
        return f"Successfully wrote to file: {file_path}"
    except PermissionError:
        logging.warning(f"Permission denied for writing to file: {file_path}")
        return f"Error: Permission denied for file: {file_path}"
    except Exception as e:
        logging.error(f"Error writing to file {file_path}: {e}")
        return f"Error: Could not write to file: {e}"

def write_multiple_files(file_contents: Dict[str, str]) -> Dict[str, str]:
    """
    Writes content to multiple specified absolute file paths.
    If directories in the paths do not exist, they will be created automatically.
    Access is restricted to pre-approved directories for each file.

    Args:
        file_contents: A dictionary where keys are the absolute file paths and values are
                      the content to write to each file.

    Returns:
        A dictionary where keys are the original file paths and values are
        either a success message (string) or an error message (string) for that specific file.
    """
    logging.info(f"Tool 'write_multiple_files' called with paths: {list(file_contents.keys())}")
    results: Dict[str, str] = {}
    for file_path, content in file_contents.items():
        # Use the existing write_file logic for each path
        result = write_file(file_path, content)
        results[file_path] = result
        # Logging is handled within write_file

    logging.info(f"Finished processing multiple files. Results count: {len(results)}")
    return results

# Test directory
test_dir = Path(os.path.join(os.path.dirname(__file__), 'test_output'))

# Test single file write
test_file1 = test_dir / 'test1.txt'
content1 = 'This is a test file 1'
print(f"Writing to {test_file1}...")
result1 = write_file(str(test_file1), content1)
print(result1)

# Test multiple files write
test_file2 = test_dir / 'subdir' / 'test2.txt'
test_file3 = test_dir / 'subdir' / 'nested' / 'test3.txt'
file_contents = {
    str(test_file2): 'This is a test file 2',
    str(test_file3): 'This is a test file 3'
}
print(f"Writing multiple files...")
results = write_multiple_files(file_contents)
for path, result in results.items():
    print(f"{path}: {result}")

# Verify by reading the files back
print("\nVerifying by reading files back:")
read_result1 = read_file(str(test_file1))
print(f"{test_file1}: {read_result1}")

read_results = read_multiple_files([str(test_file2), str(test_file3)])
for path, content in read_results.items():
    print(f"{path}: {content}")
