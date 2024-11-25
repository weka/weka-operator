# weka-k8s-api

This repo contains Weka k8s Custom Resource Definitions (CRDs) and corresponding Go type definitions.


## Setting Up Development Environment

To contribute to this project, follow these steps to set up your development environment:

### Install Pre-Commit Hooks

This project uses [pre-commit](https://pre-commit.com/) to automate checks before each commit. To set it up:

1. Install `pre-commit`:
    ```bash
    pip install pre-commit
    ```
2. Install the hooks:
    ```bash
    pre-commit install
    ```
3. (Optional) Run the hooks on all files to ensure compliance:
    ```bash
    pre-commit run --all-files
    ```
