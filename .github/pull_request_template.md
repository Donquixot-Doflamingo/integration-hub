## Ticket(s)
HUB-###

## What and why
<!-- One paragraph. Link the feature doc (docs/features/F-XX-*.md) and user stories (US-FFNN). -->

## Contract impact
- [ ] None
- [ ] Contract changed first, in its own commit (`contract(...)`), version bumped, `contracts/CHANGELOG.md` updated

## Checklist
- [ ] Branch is `<type>/HUB-###-slug`, not `main`
- [ ] Conventional Commit messages. Each commit has its own `CHANGELOG.md` entry
- [ ] Tests: unit (`-race`), integration, contract, goldens as relevant. All green
- [ ] No vendor/account/version leaks into the caller API
- [ ] No secrets in logs, fixtures or audit samples
- [ ] Feature doc, user stories, tickets (status) and ADR (if any) updated
- [ ] New metrics, logs and spans listed in `docs/tech/OBSERVABILITY.md`

## Deviations from DESIGN.md
<!-- None, or a list with ADR links -->
