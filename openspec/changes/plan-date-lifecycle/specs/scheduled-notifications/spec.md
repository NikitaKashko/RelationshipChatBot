## Purpose

Доставлять уведомления и опросы в подходящее время, сохранять расписание после перезапуска и отменять сообщения утративших актуальность планов.

## ADDED Requirements

### Requirement: Persistent scheduling
The system SHALL retain pending notifications across restart, associate them with the current plan version and recipient, and invalidate notifications after pair termination, cancellation or relevant rescheduling.

#### Scenario: Old schedule
- **WHEN** a plan changes before its old reminder is sent
- **THEN** the old reminder is invalidated and delivery uses the new plan schedule.

### Requirement: Feedback time
The system SHALL schedule one feedback prompt per partner at 12:00 on the next calendar day after the plan ends in the pair timezone, with configurable prompt time and no automatic followup if ignored.

#### Scenario: Late ending
- **WHEN** a date ends Tuesday at 01:00 in the pair timezone
- **THEN** its default prompt is Wednesday at 12:00, not Tuesday at noon.

#### Scenario: Deferred response
- **WHEN** a user ignores the prompt or chooses «Позже»
- **THEN** no repeated automatic reminder is generated; the user can return to the available feedback action.

### Requirement: Quiet hours
Each user SHALL have personal quiet hours defaulting to 23:00–09:00 in their timezone. All outbound notifications SHALL wait outside those hours, with successive pending changes to one plan coalesced. Direct replies to current user interactions SHALL remain available.

#### Scenario: Quiet feedback
- **WHEN** the pair-scheduled feedback falls inside a recipient's quiet hours
- **THEN** delivery waits for their next permitted time and remains subject to expiry.

### Requirement: Reminder and timezone changes
The system SHALL offer one disableable reminder two hours before plan start. Changing the approved pair timezone SHALL recompute future feedback schedules without changing the actual instants of saved plans.

#### Scenario: Reminder deferred too far
- **WHEN** quiet hours end only after the date starts
- **THEN** the pre-date reminder is not delivered late as if the date were upcoming.

### Requirement: Recovery windows
After outages the system SHALL send feedback only within forty-eight hours of its scheduled time, pre-date reminders only before start, and affection only within twelve hours. It SHALL avoid ordinary duplicate delivery and expose delivery failures without blocking manual functions.

#### Scenario: Telegram unavailable
- **WHEN** a recipient blocked the bot or a send fails
- **THEN** delivery state records the failure, retry behavior respects relevance windows and no infinite notification storm is generated.
