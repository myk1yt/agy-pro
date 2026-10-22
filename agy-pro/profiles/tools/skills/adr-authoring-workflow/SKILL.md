---
name: adr-authoring-workflow
description: >
  Full ADR (Architecture Decision Record) authoring cycle: design discussion, trade-off analysis, ADR generation, and execution prompt creation. Manages ADR index and ensures decisions reference project principles.
---

# ADR Authoring Workflow

## Purpose
ADR records: what problem needed solving, what constraints and decision drivers existed,
what choice was made, why that choice, what was gained and sacrificed, how to verify validity,
when to revisit or reverse. ADR is NOT: detailed implementation spec, meeting minutes,
or record of every tech choice.

## When to Use
**Create ADR when:**
- Multi-module/service/team impact
- Public contract or data model change
- High long-term maintenance cost choice
- Hard-to-reverse or migration-requiring decision
- Security/privacy/availability impact
- Multiple reasonable alternatives with important trade-offs
- Repeatedly debated decision
- Exception to existing architecture principles
- Future workers need "why"

**Don't create ADR for:** simple refactor, local implementation detail, style/naming choices,
easily reversible experiments, decisions following existing ADR,
choices sufficient for issue/comment/commit message.

## Core Principles
1. **Repository Convention First**, Check existing ADR directory, template, numbering,
   index, approval rules first. Only reference project-specific principle documents
   if they actually exist. Don't invent `ethos.md` or document hierarchies.
2. **Decision Before Documentation**, Don't write unagreed content as finalized ADR.
3. **Evidence and Assumptions**, Separate confirmed facts, assumptions, and unresolved items.
4. **Alternatives Must Be Real**, Don't force exactly 3 alternatives. Only compare
   actually viable options. Include status quo if meaningful.
5. **ADR and Implementation Plan Are Separate**, ADR records decision and outcomes.
   Concrete file lists and work sequences go in separate implementation plan.

## Flow
QUALIFY → DISCOVER EXISTING RECORDS → FRAME THE DECISION → ANALYZE REAL ALTERNATIVES
→ PROPOSE → APPROVE → RECORD → IMPLEMENT SEPARATELY → VERIFY OUTCOME → REVIEW OR SUPERSEDE

## 1. QUALIFY
Before ADR, record YAML: topic, problem, scope, urgency, affected_systems,
decision_owner, approvers, reversibility, existing_decision.
If same decision already exists, don't create new ADR, review existing ADR's
applicability, modification, or supersession.

## 2. FRAME THE DECISION
Separate: context, problem, in-scope, out-of-scope, decision drivers, constraints,
assumptions, unknowns, required decision date.

**Decision driver examples:** correctness, delivery speed, maintainability,
compatibility, performance, security, operational complexity, cost, migration effort,
vendor dependence, reversibility.

Don't mechanically use all items, select only those important to actual decision.

## 3. ANALYZE ALTERNATIVES
Compare each real alternative on same criteria:
- **Fit**, Solves problem and constraints?
- **Cost**, Implementation/operational/migration cost?
- **Risk**, Failure modes and blast radius?
- **Compatibility**, Impact on existing contracts and tooling?
- **Operations**, Deployable/observable/recoverable?
- **Security**, Permission/data/attack surface changes?
- **Reversibility**, Reversible and exit cost?
- **Evidence**, Benchmarks/prototypes/precedents?

Don't invent arbitrary scores when exact numbers unavailable, state qualitative
assessment and uncertainty. If only one viable alternative exists, don't create
fake alternatives, record why other choices are non-viable.

## 4. PROPOSAL
Include YAML: decision (one clear sentence), rationale, consequences (positive AND negative),
risks, mitigations, validation, rollback_or_exit, unresolved.
Avoid unsupported expressions like "improves maintainability".

## 5. STATUS AND APPROVAL
**States:** Proposed, Accepted, Rejected, Deprecated, Superseded.
Follow repo's existing state names if different.

- `Accepted` only when authorized decision owner or approver has approved.
- Pre-approval document is `Proposed`. AI doesn't self-approve.
- Don't fix arbitrary discussion duration, discuss until evidence and stakeholder
  alignment sufficient, but publicize timebox and unresolved items.

## 6. ADR RECORD
If no existing template, use short format:
```markdown
# ADR-[ID]: [Decision Title]
- Status / Date / Owners / Supersedes / Superseded by

## Context (problem, scope, key background)
## Decision Drivers
## Decision (one-sentence adopted decision + needed explanation)
## Alternatives (each: pros/cons/dismissed or adopted reason)
## Consequences (Positive + Negative)
## Risks and Mitigations
## Validation (metrics/tests/reviews/milestones for success judgment)
## Revisit Triggers (cost/performance/scale/policy change conditions)
## References (related specs/issues/benchmarks/prototypes/previous ADRs)
```

Don't fill empty sections formally. Don't add unverified references.

## 7. STORAGE AND INDEX
Follow repo rules if they exist. Otherwise: independent file per ADR or single decision log.

**Before writing:**
- Search existing ADRs, check ID/filename conflicts
- Verify numbering convention, check index format
- Check working-tree state

**After writing:**
- Verify ID/status/link consistency
- Check index for duplicates/omissions
- Verify supersedes links bidirectionally
- Check markdown links, verify only expected files changed

Don't silently rewrite existing ADR body. Direction changes → new ADR with supersede.
Non-meaningful fixes (typos/broken links) may be allowed per repo policy.

## 8. IMPLEMENTATION HANDOFF
Only if user also requests implementation plan. Separate from ADR.
Include only: ADR ID, implementation scope, affected contracts/components,
acceptance criteria, migration and rollback, required tests, constraints, out-of-scope.
Specify exact files only if confirmed by repo investigation.
ADR approval ≠ implementation complete. Don't auto-generate or auto-execute implementation prompts.

## 9. VALIDATION AND DRIFT
Review ADR's `Validation` items after implementation.
Distinguish: decision accepted, implementation complete, validation complete, outcome confirmed.
If actual implementation differs from ADR:
- Align implementation to ADR, OR
- Supersede ADR, OR
- Record explicit deviation

Don't secretly modify existing ADR to match current implementation.

## 10. COMPLETION + PROTOCOL
**COMPLETE when:**
- ADR necessity confirmed, no duplicate with existing decisions
- Problem/scope/drivers clear, real alternatives and trade-offs compared
- Assumptions and unknowns disclosed
- Decision/consequences/risks/validation recorded
- Correct approval status used, IDs/links/index consistent
- Implementation plan separated from ADR
- Supersession and revisit conditions present

**States:** DRAFTED, PROPOSED, ACCEPTED, BLOCKED, NOT_WARRANTED, SUPERSEDED.

**Prohibited (15 items):**
1. Creating ADR for every tech choice
2. Forcing exactly 3 alternatives
3. Artificially generating Standard/Pragmatic/Incremental options
4. Forcing arbitrary discussion duration
5. Referencing non-existent principle documents
6. Inventing repo document authority hierarchies
7. Marking unauthorized ADR as Accepted
8. Specifying implementation files without investigation
9. Mixing assumptions with facts
10. Omitting negative consequences
11. Reusing existing ADR IDs
12. Silently editing Active ADR body
13. Confusing ADR approval with implementation completion
14. Auto-executing implementation prompts
15. Hiding implementation drift

**Compact Protocol:**
CONFIRM THE DECISION WARRANTS AN ADR → READ EXISTING ADR RULES AND RELATED DECISIONS
→ DEFINE CONTEXT, SCOPE, DRIVERS, CONSTRAINTS, AND UNKNOWNS → COMPARE ONLY REAL ALTERNATIVES
→ RECORD A CLEAR PROPOSAL, CONSEQUENCES, RISKS, AND VALIDATION
→ KEEP STATUS PROPOSED UNTIL AUTHORIZED APPROVAL → WRITE USING THE REPOSITORY CONVENTION
→ VERIFY IDS, LINKS, INDEX, AND SUPERSESSION → CREATE IMPLEMENTATION HANDOFF ONLY WHEN REQUESTED
→ REVIEW OUTCOMES AND SUPERSEDE WHEN THE DECISION CHANGES
