## Purpose

Позволить менять пользовательские формулировки и коротко сообщать участникам об обновлениях без пересборки Go-ядра.

## ADDED Requirements

### Requirement: Runtime text catalog
The application SHALL load all bot-facing copy, button labels, AI prompt copy and owner-panel presentation from versioned files at process start. Editing a valid deployed content file and restarting the relevant service SHALL apply the new copy without recompiling the binary. Missing files, duplicate keys, invalid templates and missing required keys SHALL fail startup with a diagnostic instead of silently mixing old and new copy.

#### Scenario: Operator changes a button label
- **WHEN** the operator edits a valid button label in the deployed Russian catalog and restarts the bot
- **THEN** the new label is displayed and accepted by the menu without rebuilding the Go binary.

#### Scenario: Invalid catalog
- **WHEN** a required key is absent or the JSON is invalid
- **THEN** the service refuses to start and the previous content file can be restored without changing the database.

### Requirement: Idempotent release announcement
The bot SHALL support an optional concise release announcement loaded from a runtime file. On startup it SHALL enqueue that announcement once for every ready user using a stable release ID and durable semantic deduplication. Restarts with the same ID SHALL not repeat delivery; changing the ID SHALL define a new announcement. Ordinary quiet hours and delivery recovery rules SHALL apply.

#### Scenario: Repeated restart
- **WHEN** the bot restarts several times with the same enabled release announcement
- **THEN** each ready user has at most one queued or delivered notification for that release ID.

#### Scenario: New functional release
- **WHEN** the operator deploys an enabled announcement with a new non-empty ID and short text
- **THEN** every currently ready user receives the concise explanation and usage hint through the durable notification queue.
