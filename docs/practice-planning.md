# Practice planning proposal

Current saved direction: `/jazz/plan/`. September 10 transcription is a date-specific curriculum addition. Neither is an editable cloud planner yet.

## Recommendation

One Plan destination with two coordinated views: Schedule (dated practice) and Progression (musical goals, milestones, and undated ideas). Today shows only actionable practice plus a small next-step preview. Clip Studio keeps recordings and finished films.

Calendar alone invents deadlines for mastery. A progression board alone hides actual commitments. Combine a dated session list with a Now / Next / After milestone progression; provide a calendar as an alternate view of those same scheduled items.

## Data model

- Plan item: stable ID, user, title, purpose, reference links, tune, status (idea/active/completed/archived), optional target BPM, notes, revision.
- Scheduled occurrence: stable ID, plan item ID, local date and timezone, target minutes, placement, state (scheduled/started/completed/skipped). Keep dates separate from timestamps.
- Milestone: plan item ID, editable criteria, optional supporting take IDs, explicitly confirmed completion and timestamp.
- Dependency: prerequisite milestone → successor plan item. Completing a milestone makes a successor ready; it never silently starts, schedules, or marks mastery. Reject cycles and self-dependencies.
- Practice block: link to originating occurrence and a snapshot of its instructions. Preserve history when the parent plan changes.

## Behavior and edges

- Saved plan data belongs in dedicated authenticated API tables, not curriculum source files or campaign JSON (the existing campaign API rejects unknown fields).
- Opening a day creates its blocks idempotently with a unique occurrence-to-block association. Repeated loads or two devices cannot duplicate sections.
- Reschedule an untouched occurrence freely. Once a recording or practice progress exists, keep that day's work and offer an additional session on the new date. Do not move recordings to another day.
- Removing a daily section leaves its parent goal and recordings intact. Offer skip today, reschedule, or archive the plan as distinct actions. Keep existing active-recording deletion protections.
- Missed sessions stay available to reschedule; no silent daily rollover and no punitive mastery reset. Undated next steps never become overdue.
- Recurrence is optional later. First version needs dated one-offs, undated goals, dependencies, references, and explicit milestone confirmation.
- Optimistic revisions protect concurrent phone/desktop edits. Offline drafts must say unsynced and may not claim a date is booked before server acceptance.
- Attach manuscript photos, notation files/PDFs and takes to the same transcription goal in a later iteration. Start with reference links and text notes.

## Rollout

1. Preserve current intentions on the site (done in this change); review the interactive design prototype.
2. Build authenticated plan/occurrence/milestone CRUD, dependency validation, and conflict handling. Migrate the September 10 item with a stable ID and no duplicate existing block.
3. Add Plan navigation and a Today preview. Validate two-device scheduling, reload deduplication, day boundaries, missed sessions, and deletion with recordings.
4. Add evidence attachments and calendar polish after the core workflow is useful.

Open choices: Blue Bossa target BPM/backing track and the second standard. The three-chorus, two-day mastery check is a suggestion, not a confirmed achievement or mandatory requirement.
