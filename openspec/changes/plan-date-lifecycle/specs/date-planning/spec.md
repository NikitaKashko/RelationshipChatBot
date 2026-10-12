## Purpose

Превращать общую коллекцию в черновики и утверждённые свидания, позволяя обоим партнёрам менять планы без потери чужих изменений.

## ADDED Requirements

### Requirement: Guided pair timezone prerequisite
The system SHALL require a confirmed pair timezone before approving a dated plan. When it is missing, save SHALL preserve the pending draft, explain the exact prerequisite and offer a button-led pair city and timezone proposal flow rather than a generic failure.

#### Scenario: Approving before pair location confirmation
- **WHEN** a partner enters a valid date for a draft while the pair timezone is not confirmed
- **THEN** the plan remains a draft, the bot identifies the missing pair setting and opens city then timezone entry; the other partner must confirm the proposal before approval can be retried.

### Requirement: Date drafts and approved dates
The system SHALL keep a shared collection, separate shared date drafts and approved dates with history. Drafts SHALL need a title or idea, but no places, route or date. AI proposals SHALL remain private until saving; saving creates a shared draft and one partner notification. Either partner SHALL edit shared drafts with version checks and one notification per saved change. Former candidates SHALL be migrated into a draft without losing places.

#### Scenario: New draft from an idea
- **WHEN** a user saves a draft such as an evening at home
- **THEN** it becomes a shared draft without automatic reminders or a visit.

### Requirement: Scheduled plan details
An approved date SHALL require a calendar date; places, route, start time and end time SHALL be optional. Either partner SHALL approve a draft without a second approval, with one notification to the other. Relative dates SHALL be displayed as exact calendar dates before saving.

#### Scenario: After midnight
- **WHEN** the end falls on the day after the start
- **THEN** the preview clearly shows both calendar dates and validates that the end is after the start.

### Requirement: Saving and notification
Either partner SHALL save or update plans without the other's approval. Saving SHALL produce one partner notification summarizing the plan and changed fields. Unsaved edits SHALL not notify; saving a shared draft or approved date SHALL notify once. AI changes SHALL remain private proposals until one explicit save action.

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
