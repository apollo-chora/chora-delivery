# Legacy migrations (B4.b — M12.2.C)

Per the M12.2 consolidation plan, the 5 legacy top-level Go services
(chora-classroom, chora-training-admin, chora-examadmin, chora-campusops,
chora-wbl) were merged into `services/chora-delivery` on 2026-05-12.

This directory preserves the **original golang-migrate** SQL files from each
legacy service. They are intentionally **NOT** picked up by the chora-delivery
migration runner — the canonical migration sequence in `..` (parent dir)
already owns the native `course / class / booking / attendance / certification`
aggregates, and the legacy tables (live_quiz_sessions, training_sessions,
exam_contracts, academic_terms, internships, capstones, etc.) need careful
integration to avoid extension / processed_events conflicts.

## Layout

```
migrations/legacy/
├── classroom/        — 12 files (live quizzes, polls, jam boards, class profiles)
├── training_admin/   — 16 files (training sessions, attendance, applications, certs)
├── examadmin/        — 18 files (exam contracts, sittings, candidates, proctoring, diagnostics)
├── campusops/        — 14 files (academic terms, venues, sections, bookings, attendance)
└── wbl/              — 16 files (internships, applications, placements, work logs, capstones)
```

## Disposition (M12.3 follow-up)

The M12.3 plan ("per-domain outbox application + integration") will:

1. Audit each legacy table for live data needs.
2. Renumber & merge in-scope SQL files into the canonical `migrations/` sequence
   under new ordinals (e.g., `0010_classroom_*.sql`, `0020_training_admin_*.sql`,
   …). Each migration is rewritten to reference the chora_delivery schema +
   shared RLS helper functions.
3. Deduplicate processed_events / extension creation across the 5 legacy
   sequences against the canonical `0003_outbox.sql`.
4. Adopt `outbox_events` (canonical) over each legacy `processed_events`
   table in lockstep with the M12.3 outbox migration template.
5. Add RLS policies aligned with `app.current_tenant_id` per
   `.claude/skills/multi-tenant-rls`.
6. Author a single `down` plan for the merged sequence.

Until M12.3 closes, the legacy SQL here is **reference-only** and is read by
the consolidation_test.go in `internal/legacy/` only as a layout assertion.

## Open follow-ups

- [ ] Resolve overlapping extension / trigger names across the 5 services.
- [ ] Pick canonical RLS helper (`current_setting('app.current_tenant_id')`).
- [ ] Unify processed_events into outbox_events idempotency table.
- [ ] Reconcile Course / Class naming collision (chora-classroom uses
      `class_profiles`; chora-training-admin uses `training_sessions`;
      native chora-delivery uses `courses` + `classes`). See report
      addendum #5 in the B4.b sub-task completion.
