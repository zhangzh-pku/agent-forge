## Summary

- What changed:
- Why this change is needed:

## Validation

- [ ] `make ci` passed locally for Go/runtime changes
- [ ] Added/updated tests for behavior changes
- [ ] Security impact reviewed (authz/data exposure/dependencies)
- [ ] Docs/config updated when interfaces or behavior changed

## Change-Type Checklist

- [ ] Go runtime/API/engine change
  Required: `make ci`
- [ ] Terraform change (`deploy/terraform/**`)
  Required: `terraform -chdir=deploy/terraform fmt -check -recursive` and `terraform -chdir=deploy/terraform validate`
  Include: redacted `terraform plan` summary in this PR
- [ ] Docs-only change
  Required: verify referenced commands/paths exist in current repo

## Rollback Plan

- How to rollback safely if this causes issues:
