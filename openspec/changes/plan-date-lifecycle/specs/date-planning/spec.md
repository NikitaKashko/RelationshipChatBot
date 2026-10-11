## Purpose

Превращать общие идеи в кандидатов и сохранённые планы свиданий, позволяя обоим партнёрам менять планы без потери чужих изменений.

## ADDED Requirements

### Requirement: Candidates and plans
The system SHALL keep one shared next-date candidate list, several scheduled plans and past-plan history. Both partners SHALL edit candidates silently. An AI shortlist SHALL enter candidates only through «Взять за основу» or equivalent explicit action.

#### Scenario: Internet candidate
- **WHEN** a user accepts an internet result into candidates
- **THEN** it is also saved in the collection, but does not become a scheduled or completed date.

### Requirement: Scheduled plan details
A saved plan SHALL contain at least one place or activity, ordered steps, date and approximate start and end. Relative dates SHALL be displayed as exact dates and times before saving. Ideas and candidates SHALL not require scheduling.

#### Scenario: After midnight
- **WHEN** the end falls on the day after the start
- **THEN** the preview clearly shows both calendar dates and validates that the end is after the start.

### Requirement: Saving and notification
Either partner SHALL save or update plans without the other's approval. Saving SHALL produce one partner notification summarizing the plan and changed fields. Draft edits SHALL not notify; AI changes SHALL remain drafts until one explicit save action.

#### Scenario: Voice reschedule
- **WHEN** a user says «перенеси на пятницу»
- **THEN** the bot prepares a dated preview and sends a partner notification only after «Сохранить».

### Requirement: Concurrent edits
The system SHALL check the version being edited when saving and SHALL not silently overwrite a newer partner version.

#### Scenario: Stale draft
- **WHEN** the partner saved while the user's draft was open
- **THEN** the bot displays the newer plan and invites reapplication of the change instead of saving the stale draft.

### Requirement: Cancellation and rescheduling
Cancellation and rescheduling SHALL notify the other partner and cancel or replace pending reminders and feedback prompts. Not visiting a planned place SHALL leave it eligible unless explicitly frozen or refused.

#### Scenario: Never went
- **WHEN** users mark «Не пошли»
- **THEN** no visit or automatic freeze is recorded and the idea remains in the collection.

### Requirement: Manual choices
The system SHALL allow manual selection of frozen or personally refused places, show the relevant warning, and preserve the freeze or personal refusal. Manual selection SHALL not be treated as automatic recommendation eligibility.

#### Scenario: Choosing a favorite again
- **WHEN** a user explicitly picks a frozen card into a plan
- **THEN** it is accepted without clearing the freeze and remains excluded from unrelated automatic recommendations.
