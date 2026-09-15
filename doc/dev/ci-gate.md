# ci-gate: the required check for stacked PRs

`ci-gate` is the only required status check on `main`. It is produced by
`.github/workflows/ci_gate.yaml`, which runs the upgrade-extended and clients-only tests on
the lab cluster. Stacked PRs (GitHub native stacks, `gh stack`) are tested once per stack, on
the top layer, and lower layers cannot be merged untested. There is no merge queue.

## How it works

- `ci-gate` is a **commit status** on the PR's head commit, posted by the workflow's
  `report_status` job. No job is named `ci-gate`. A PR with no status shows "Expected" and
  cannot be merged.
- The `gate` job runs only for same-repo, non-draft PRs that are not stacked, are the top of
  their stack, or carry `run_ci_gate`. It decides between **test** and **skip**:
  `skip_ci_gate` label, or all commits since the stack base are chore/docs, means skip.
- Before testing, the layers below are set to `pending`; when the tests pass they are set
  to `success`, so "Merge stack" on the tested layer can merge everything below it.
- A skip counts as a pass for that PR. A lower layer inherits the pass only if it is
  skippable on its own (same label, or its own commits are all chore/docs).
- One execution at a time on the lab cluster: runs of different PRs wait for each other.
  A new push to a PR cancels that PR's running job and stops its remote execution.

## Expected behaviour

Stack notation: `main <- A <- B <- C`, C is the top. "Expected" means no status, blocked.

| Case | A | B | C | Notes |
|---|---|---|---|---|
| Non-stacked PR, ready | tests run, result on the PR | | | as before |
| Stack submitted as drafts, `gh pr ready C` | pending, then success | pending, then success | tests run | one run for the stack |
| Stack submitted ready (`gs submit --open`) | own run starts, cancelled by C's run | same | tests run | drafts avoid the extra runs |
| C is a draft | Expected | Expected | no run | nothing below a draft top can merge |
| C's tests fail | pending | pending | failure | nothing merges; push a fix to C or below |
| Push to A while C is green | Expected (new SHA) | stale | stale, needs rebase | `gs sync`, then C re-tests and re-greens A and B |
| Amend C while its tests run | pending | pending | old run cancelled, new run tests | remote execution of the old run stopped |
| `run_ci_gate` on B | pending, then success | tests run, own result | untouched | "Merge stack" on B lands A+B; remove the label after |
| `skip_ci_gate` on C, A and B have code commits | Expected | Expected | success | skip does not reach non-skippable layers |
| `skip_ci_gate` on C, A is chore-only, B has code | success ("chore/docs only") | Expected | success | only the skippable layer inherits |
| All commits in the stack are chore/docs | success | success | success (no tests) | same as skip on C |
| `skip_ci_gate` on A alone | success | Expected | Expected | A mergeable alone; B and C still need C's run |
| Merge A+B via "Merge stack" on B | merged | merged | retargeted and rebased by GitHub, re-tests | new top re-tests, no manual sync needed |
| Close C without merging | stale | stale, still "2 of 3" | closed | recreate the stack with A and B (closed PRs stay in it) |
| Fork PR | Expected | | | never runs, never skips; push the branch into this repo to test |
| Someone posts a `ci-gate` status by hand | ignored | | | the ruleset accepts the context only from GitHub Actions |

## Labels

| Label | Effect |
|---|---|
| `run_ci_gate` | Run the tests on this PR even if it is a lower layer; layers below it are held and greened like for a top. |
| `skip_ci_gate` | Count the tests as passed for this PR without running them. Reaches lower layers only if they are skippable themselves. |

## Known limits

- Once the layer above is green, a layer can be merged alone until the stack merge happens.
  GitHub requires the layer's own state to be mergeable for the stack merge, so this window
  cannot be closed while humans keep the Merge button.
- Removing `skip_ci_gate` or `run_ci_gate` from a **lower** layer leaves that layer's existing
  green in place, so it stays mergeable alone. The stack top's run covers that layer, so nothing
  re-tests it here; push to the layer, or re-run the top, to clear it. Removing either label from
  a stack top or a non-stacked PR does re-evaluate it.
- Cancelling a job takes GitHub 20 to 50 seconds to take effect.
- Old PRs whose branch predates `ci_gate.yaml` cannot report the status; rebase them.
