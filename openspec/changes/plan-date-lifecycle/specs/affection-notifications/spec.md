## Purpose

Позволить партнёрам отправлять ненавязчивые персональные знаки внимания одним нажатием, с соблюдением времени и настроек получателя.

## ADDED Requirements

### Requirement: Button initiated affection
The system SHALL provide «Хочу поцеловать», «Думаю о тебе» and «Хочу обнять». The former «Хочу внимания» action SHALL NOT be offered or accepted. A single click SHALL enqueue the corresponding message without text entry, confirmation or an AI request. Affection SHALL never be initiated automatically.

#### Scenario: Removed attention action
- **WHEN** the affection menu is rendered or a legacy attention callback is received
- **THEN** the menu contains the kiss action instead and the legacy action does not enqueue a message.

#### Scenario: Click without active pair
- **WHEN** a user without an active pair clicks an affection button
- **THEN** the bot explains that pairing is needed and sends no affection message.

### Requirement: Names and variations
The system SHALL use the sender's authenticated Telegram profile display name by default. Only the recipient SHALL choose or reset a partner nickname for messages they receive; the sender SHALL NOT configure an outgoing signature. Existing sender aliases SHALL NOT affect affection. Each button SHALL have five to seven prepared phrases selected randomly without repeating its immediately previous phrase.

#### Scenario: Recipient nickname
- **WHEN** the sender's Telegram profile name is «Никита» and the recipient assigned «зайка»
- **THEN** the recipient's message uses «зайка» and the bot generates no paid AI call.

### Requirement: Frequency and delivery
The system SHALL accept at most one affection per sender per minute across all three buttons. Accepted messages SHALL bypass recipient quiet hours and expire after twelve hours without delivery.

#### Scenario: Quiet hours
- **WHEN** an accepted affection occurs during recipient quiet hours
- **THEN** delivery is immediately eligible, independently from quiet hours.

### Requirement: Isolated two-role simulator
The system SHALL offer a separate test bot and database restricted to one authenticated owner account, mapping two virtual roles through the regular domain services. Production routing SHALL NOT accept virtual identities. Test messages SHALL identify their recipient role, inline callbacks SHALL retain their originating role, and outbound recipients SHALL be restricted to the test owner.

#### Scenario: Old role button
- **WHEN** the owner switches to role B and clicks a button originally sent to role A
- **THEN** the action runs as role A, not role B; foreign accounts and unmarked callbacks are rejected.
