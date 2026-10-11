## Purpose

Позволить партнёрам отправлять ненавязчивые персональные знаки внимания одним нажатием, с соблюдением времени и настроек получателя.

## ADDED Requirements

### Requirement: Button initiated affection
The system SHALL provide «Хочу внимания», «Думаю о тебе» and «Хочу обнять». A single click SHALL enqueue the corresponding message without text entry, confirmation or an AI request. Affection SHALL never be initiated automatically.

#### Scenario: Click without active pair
- **WHEN** a user without an active pair clicks an affection button
- **THEN** the bot explains that pairing is needed and sends no affection message.

### Requirement: Names and variations
The system SHALL let senders choose their notification name and recipients override their partner's name. The recipient override SHALL take priority. Each button SHALL have five to seven prepared phrases selected randomly without repeating its immediately previous phrase.

#### Scenario: Recipient nickname
- **WHEN** the sender uses «Никита» and the recipient assigned «зайка»
- **THEN** the recipient's message uses «зайка» and the bot generates no paid AI call.

### Requirement: Frequency and delivery
The system SHALL accept at most one affection per sender per minute across all three buttons. Accepted messages SHALL obey recipient quiet hours and expire after twelve hours without delivery.

#### Scenario: Quiet hours
- **WHEN** an accepted affection occurs during recipient quiet hours
- **THEN** it waits until those hours end and is discarded if it would be older than twelve hours.
