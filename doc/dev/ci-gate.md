# ci-gate: the required check for stacked PRs

`ci-gate` is the only required status check on `main`. It is produced by
`.github/workflows/ci_gate.yaml`, which runs the upgrade-extended and clients-only tests on
the lab cluster. Stacked PRs (GitHub native stacks, `gh stack`) are tested once per stack, on
the top layer, and lower layers cannot be merged untested. There is no merge queue.

## How it works

- `ci-gate` is a **commit status** on the PR's head commit, posted by the workflow's
  `report_status` job. No job is named `ci-gate`. A PR with no status shows "Expected" and
  cannot be merged.
- The `gate` job runs for every same-repo, non-draft PR and reaches one of three verdicts:
  - **skip** - counts as a pass, no tests. The `skip_ci_gate` label, or a commit range that is
    all chore/docs.
  - **test** - runs the lab tests. The top of a stack, a non-stacked PR, or any layer carrying
    `run_ci_gate`.
  - **blocked** - no status at all, so the PR shows "Expected". A lower layer whose own commits
    need a test: the top's run covers it.
- Which commit range is inspected depends on position. A stack top (or non-stacked PR) inspects
  everything since the **stack base**, because its verdict covers every layer below it. A lower
  layer inspects only **its own** commits (`base..head`; in a stack a PR's base ref is the layer
  below it), so a chore/docs-only layer passes itself and never waits for the layer above. The
  top's run greens such layers too (it computes the same per-layer range), so the outcome does
  not depend on which run finishes first.
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
| Stack submitted ready (`gs submit --open`) | own run starts, cancelled by C's run (or success right away if chore/docs-only) | same | tests run | drafts avoid the extra runs |
| C is a draft | Expected, or success if A is chore/docs-only | Expected, or success if B is chore/docs-only | no run | a draft top never runs; each layer below it is judged on its own commits, so A green does not make B green |
| B is docs-only, A and C have code | pending, then success | success (no tests) | tests run | B does not wait for C |
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

- A lower layer with code commits has no status of its own until the top's run greens it. That
  is deliberate: only the top runs the stack's test. Label it `run_ci_gate` to give it its own
  run, or `skip_ci_gate` to pass it without one.
- Once the layer above is green, a layer can be merged alone until the stack merge happens.
  GitHub requires the layer's own state to be mergeable for the stack merge, so this window
  cannot be closed while humans keep the Merge button.
- Removing `skip_ci_gate` or `run_ci_gate` from a **lower** layer re-evaluates it: a skip-based
  green (its own, or mirrored by a top) is reset to `pending` if its own commits need a test, and
  the layer waits for the top again. A green from a passed test is left in place.
- If the layer below was force-pushed and this layer has not been restacked yet, its own range
  still contains the dropped commits, so a docs-only layer can read as "has code" and be blocked
  until `gs sync`. Safe direction, just a stale verdict.
- Cancelling a job takes GitHub 20 to 50 seconds to take effect.
- Old PRs whose branch predates `ci_gate.yaml` cannot report the status; rebase them.
