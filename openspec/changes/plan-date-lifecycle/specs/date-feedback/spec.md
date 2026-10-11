## Purpose

Сохранять реальные посещения и личные впечатления, чтобы рекомендации учитывали повторные походы, отказ одного партнёра и временный фриз.

## ADDED Requirements

### Requirement: Actual visits
The system SHALL require an explicit visit fact instead of inferring it from elapsed plans. Either partner SHALL mark visited plan places for the pair; the other SHALL then be asked only for impressions. Each repeat visit SHALL have a separate record and personal reviews.

#### Scenario: Partial date
- **WHEN** only two of three planned places were visited
- **THEN** only those two receive visits and automatic freeze; the third remains eligible.

### Requirement: Manual historical visits
Users SHALL record past and spontaneous visits with exact or approximate dates, optional personal reviews and an explicit choice of freeze. Manually added visits SHALL not generate an automatic feedback prompt.

#### Scenario: Approximate old visit
- **WHEN** the user records a visit from an approximate past month
- **THEN** the date precision is retained and the bot does not claim a precise day or schedule retrospective prompts.

### Requirement: Individual reviews
Each author SHALL control their own «Понравилось / Нейтрально / Не понравилось», «Хочу ещё / Не скоро / Больше не хочу» and optional comment. AI interpretation of free text SHALL require confirmation before storing ratings or changing related state.

#### Scenario: Ambiguous comment
- **WHEN** the user writes a mixed impression
- **THEN** the bot offers its interpretation for confirmation and leaves the previous rating unchanged until accepted.

### Requirement: Automatic and manual freeze
Confirmed scheduled visits SHALL automatically freeze a place for thirty days from its visit date. Either partner SHALL change or remove the common freeze with one notification to the other. Manual overrides SHALL be retained separately; «Хочу ещё» SHALL offer, not silently apply, a shorter duration.

#### Scenario: Repeated visit
- **WHEN** a place is visited again
- **THEN** its automatic freeze derives from the newest valid visit and an existing manual override is not silently lost.

### Requirement: Visit correction
Either partner SHALL correct a visit fact with notification to the other. Automatic freeze SHALL be recalculated from the latest remaining valid visit while preserving explicit manual overrides.

#### Scenario: Erroneous visit removed
- **WHEN** the most recent visit is corrected as not having occurred
- **THEN** the automatic deadline uses the previous valid visit or no automatic freeze if none remain.

### Requirement: Personal refusal
A «Больше не хочу» from either partner SHALL exclude a place from automatic pair recommendations while leaving it visible and manually selectable with a warning. Only its author SHALL change that refusal.

#### Scenario: Freeze removal with refusal
- **WHEN** the common freeze is removed but a personal refusal remains
- **THEN** automatic recommendations still exclude the place.
