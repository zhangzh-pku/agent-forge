# Coding Standards

## Go Style

- Always run `gofmt` on modified files.
- Keep imports grouped and minimal.
- Exported identifiers must have doc comments.
- Error strings should be lowercase and without trailing punctuation.

## Project Conventions

- Put domain types in `pkg/model`.
- Keep command entrypoints (`cmd/*`) thin; prefer reusable logic in `pkg/*`.
- Centralize environment parsing in `internal/config`.
- Prefer interfaces (`pkg/state`, `pkg/queue`, `pkg/artifact`) for backend implementations.

## Testing

- Add/adjust tests for behavior changes.
- Run `make ci` before opening a PR.
- Keep tests deterministic and isolated from external cloud dependencies by default.

## Pull Request Expectations

- Small, focused diffs.
- Clear changelog in PR description: what changed, why, and how validated.
- Backward compatibility considerations called out explicitly.
