Usage examples:
- `run-upgrade-extended-test`
```
./dagger-scalar call --progress=plain --interactive --mod .dagger/src run-upgrade-extended-test --operator . --testing ../weka-k8s-testing --sock "$SSH_AUTH_SOCK" --wekai ../wekai --pr-number XXX --gh-token "env://GITHUB_PAT_TOKEN" --openai-api-key "env://OPENAI_API_KEY" --gemini-api-key "env://GEMINI_API_KEY" --kubeconfig-path $KUBECONFIG_PATH export --path ./test-artifacts
```

- `deploy-scalar`
```
./dagger-scalar call --interactive deploy-scalar  --operator=. --sock="$SSH_AUTH_SOCK" --kubeconfig=file://$KUBECONFIG
```
