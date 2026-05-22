

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:7510c1e2 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/SYNC_CONCEPTS.md for details and anti-patterns.

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
<!-- END BEADS INTEGRATION -->


## Project Architecture Rules

This project uses a 3-layer backend architecture:

1. **Handler layer**
2. **Service layer**
3. **Repository layer**

### General Rules

- Keep code as simple as possible.
- Prefer clear, readable code over clever abstractions.
- Add comments only for complex logic or non-obvious decisions.
- Do not add comments for obvious code.
- Keep each layer focused on its own responsibility.

### Handler Layer Rules

Handlers are responsible only for HTTP/request handling.

Handlers may:

- Read request data.
- Validate request input.
- Call the service layer.
- Handle service errors and map them to HTTP responses.
- Return responses to the client.

Handlers must NOT:

- Contain business logic.
- Call repositories directly.
- Access the database directly.
- Make business decisions.
- Perform data persistence logic.

### Service Layer Rules

Services contain all business logic.

Services may:

- Apply business rules.
- Coordinate repositories.
- Transform domain data.
- Decide what should happen based on business requirements.
- Return results or errors back to handlers.

Services must NOT:

- Write HTTP responses directly.
- Depend on handler/request/response objects.
- Return client responses directly.
- Contain raw database query logic.

### Repository Layer Rules

Repositories are responsible only for database access.

Repositories may:

- Create, read, update, and delete database records.
- Run database queries.
- Map database records to domain/model structs.

Repositories must NOT:

- Contain business logic.
- Handle HTTP requests or responses.
- Validate request payloads.
- Decide business rules.
- Return user-facing responses.

### Dependency Direction

The dependency flow must stay one-way:

```txt
Handler → Service → Repository → Database
```

Do not skip layers.

Bad:
```txt
Handler → Repository
Handler → Database
Repository → Service
Service → Handler
```

Good:
```txt
Handler receives request
Handler validates input
Handler calls Service
Service applies business logic
Service calls Repository
Repository talks to DB
Repository returns data
Service returns result
Handler returns HTTP response
```