---
name: architecture-constraint-setup
description: >
  Guide for defining project-specific architectural constraints at project kickoff. Helps populate AGENTS.md with appropriate rule sections based on project type and tech stack.
---

# Architecture Constraint Setup

## Purpose
Guide the process of defining project-specific architectural constraints that all
future work on the project must follow. Converts general best practices into
concrete, enforceable rules stored as sections in `AGENTS.md` (or the project's
equivalent instruction file), so every session reads them automatically.

## When to Use
- At the start of a new project (mandatory for moderate+ complexity)
- When onboarding to an existing project without constraints defined
- When a major architectural change requires new constraints
- When security or compliance requirements change

## Setup Process

### Step 1: Identify Project Type
Determine which constraint categories apply:

| Project Type | Required Categories |
|---|---|
| Multi-Tenant SaaS | ALL categories |
| Single-Tenant API | DB Access, Auth, Error Handling |
| CLI Tool | Error Handling, Naming |
| Frontend SPA | Auth, Naming |
| Library/SDK | Naming, Error Handling |

### Step 2: Define Constraints Per Category

#### Database Access Patterns
Ask these questions:
- How should database connections be named/scoped?
- Is row-level security (RLS) required?
- Are there separate roles for migration vs runtime?
- What is the connection pooling strategy?
- Are there read replica vs write primary distinctions?

Example constraints:
```text
- All *sql.DB handles MUST be named rawDB/RawDB
- Migration uses a superuser role; runtime uses a non-superuser role
- Connection pool max: 25 (configurable via env var)
```

#### Tenant Isolation
Ask these questions:
- How is multi-tenancy enforced? (schema, row, database)
- What queries must be tenant-scoped?
- Is there a build-time check for tenant isolation?
- What happens when tenant context is missing?

Example constraints:
```text
- Tenant queries MUST go through a row-level-scoped querier
- Build-time check MUST verify no direct DB access bypasses RLS
- Pre-auth flows use rawDB with explicit //rq:RLS reason annotation
```

#### Authentication & Authorization
Ask these questions:
- Where does auth live? Internal or external IdP?
- What middleware must all endpoints use?
- How are permissions checked? (RBAC, ABAC, simple roles)
- What is the session/token strategy?

Example constraints:
```text
- Auth is fully externalized via IdP, no passwords stored in app
- All endpoints must use auth middleware unless explicitly exempted with ADR
- Permission checks happen at the handler level, not the service level
```

#### Error Handling
Ask these questions:
- What is the error code format?
- Are stack traces allowed in production responses?
- How are errors logged vs returned to users?
- Is there a central error registry?

Example constraints:
```text
- All errors use MODULE/function/NNN format
- Stack traces are NEVER included in production API responses
- All errors are logged with full context; user-facing messages are sanitized
```

#### Naming Conventions
Ask these questions:
- Are there critical naming patterns that must not be violated?
- What are the file/class/function naming standards?
- Are there domain-specific naming requirements?

Example constraints:
```text
- Database handles: rawDB/RawDB (enforced by build-time check)
- API handlers: one file per domain, named after the domain
- Test files: *_test.go or *.test.ts, co-located with source
```

### Step 3: Write to AGENTS.md
Add the defined constraints as dedicated sections in the project's `AGENTS.md`
(create it at the repo root if absent). Suggested layout:

```markdown
## Architecture Constraints

### Database Access
- <constraint>

### Tenant Isolation
- <constraint>

### Authentication & Authorization
- <constraint>

### Error Handling
- <constraint>

### Naming Conventions
- <constraint>
```

Keep each constraint a single, testable bullet. If the project already has an
architecture rules file referenced from AGENTS.md, add a pointer there instead
of duplicating content.

### Step 4: Connect to Enforcement
- **Implementation**: the constraints are part of the pre-implementation checklist, verify each change against them before writing code
- **Debugging/diagnosis**: check for constraint violations when a defect touches a constrained area
- **Design/review**: apply when designing new modules or reviewing changes
- **Quality gates**: include constraint checks in any review or gate workflow

## Constraint Violation Protocol
When a constraint is violated:
1. Do NOT silently work around it
2. Escalate with: "Architecture constraint violation: [constraint], [what happened]"
3. The user/lead decides: fix the violation, or create an ADR to formally relax the constraint
4. Never violate a constraint without explicit, documented approval

## Rules
- Constraints must be specific and testable, "use good patterns" is not a constraint
- Each constraint should be verifiable during implementation
- Update constraints when architecture evolves, stale constraints are worse than no constraints
- Constraints are project-specific, do not copy blindly between projects
