## Purpose

Дать владельцу обзор работоспособности и расходов, управление приглашениями и резервным копированием без доступа к личной переписке пользователей.

## ADDED Requirements

### Requirement: Local owner panel
The system SHALL provide a mobile-usable web dashboard on the bot VPS accessible only through an SSH tunnel to a loopback listener. It SHALL display no secret API credentials or personal conversation and review texts.

#### Scenario: External access
- **WHEN** someone connects to the VPS public address on the dashboard port
- **THEN** the dashboard is unavailable; the authorized owner can reach it through the prescribed tunnel.

### Requirement: Operational infographic
The dashboard SHALL show bot health, last backup status, daily DeepSeek and search usage, request and error counts, pending notifications and totals of users, pairs, ideas and dates. Monetary calculations are deferred in the prototype.

#### Scenario: Missing usage accounting
- **WHEN** a failed provider request returns no token usage
- **THEN** the panel exposes accounting uncertainty instead of treating the cost as a known zero.

### Requirement: Owner actions
The dashboard SHALL let the owner issue and revoke access invitations and trigger a backup with its result. Future quota controls SHALL remain disabled initially. Actions SHALL be protected against unintended requests from other web origins.

#### Scenario: Backup failure
- **WHEN** a manually requested backup fails
- **THEN** the panel shows its failure and does not replace the last successful backup status with a false success.

### Requirement: Local backup rotation
The system SHALL create daily consistent database backups on the same VPS, retain seven daily and four weekly copies, and retain no backup past four weeks. User photos SHALL use Telegram file references without a dedicated permanent photo store.

#### Scenario: Restore check
- **WHEN** a retained backup is restored in a test environment
- **THEN** database integrity and foreign keys are checked; the prototype rejects snapshots with pending deletions until independent replay is implemented.

#### Scenario: Disk pressure
- **WHEN** there is insufficient space for a new backup
- **THEN** failure is visible in the panel and valid existing backups are not silently reported as freshly completed.
