# Validation & Admission

**Path**: `internal/validation/` + `internal/admission/`

Admission-webhook validators implement the `Validator` interface (`validator.go`), are
listed per-CRD in `registry.go`, and get a default severity in `admission/defaults.go`.
`doc.go` holds the sizing-mode glossary and the rule-ownership map — read it before adding a rule that
touches drive counts, container counts, or core sizing, so one condition is not reported twice.
Add a rule = implement + register + add to the defaults table. Reuse the shared helpers rather than
re-deriving: `role_specs.go` (`rolesForTemplate` — the six per-role sizing fields, one table for every
per-role validator), `template_cores.go` (`templateCoreSides` — drive/compute core totals plus the
planner-managed exclusion), `drive_role_nodes.go` (`listDriveRoleNodes` / `driveRoleNodeInfos`).
clusterCapacity validators:
`cluster_capacity_chunk_feasibility.go` (greenfield per-FD TLC share ≥ 384 GiB; skipped once the
cluster has TLC-bearing drive containers) and `cluster_capacity_protection.go` (min SW≥3, RL≥2, HS≥0 /
hotSpare optional — the `3+2+0` floor from `allocator.MinProtectionFloor`).
Protection values are resolved via `DriveSharingConfig.EffectiveProtection` (env.go): a per-cluster
spec field wins when non-zero (0 is treated as unset), else the Helm-level default (`PROTECTION_STRIPE_WIDTH` /
`PROTECTION_REDUNDANCY_LEVEL` / `PROTECTION_HOT_SPARE`, values `protection.*`) fills it. Same helper
is used in `FormCluster` so validation and formation agree.

Pod syntax rules live in `*_podspec_syntax.go` and shared `podspec_syntax.go`.
Auto-full-drives feasibility, minimum nodes, sizing-mode transitions and core limits
are mapped in `registry.go`; sizing terminology and ownership belong in `doc.go`.
