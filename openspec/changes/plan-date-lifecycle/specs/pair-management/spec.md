## Purpose

Обеспечить регистрацию по приглашениям, осознанное объединение двух пользователей и разделение личных и общих данных на протяжении жизни пары.

The prototype ships link registration and pairing. Separation, export and account deletion below are future requirements and are not exposed in the prototype.

## ADDED Requirements

### Requirement: Invitation enrollment
The system SHALL distinguish owner-issued access invitations from partner invitations. Links SHALL be single-use, expire after seven days, support revocation and replacement, and bind to the first Telegram account starting enrollment.

#### Scenario: Interrupted registration
- **WHEN** a user starts registration and returns later before completing it
- **THEN** the same account can resume; another account cannot claim the bound invitation; an expired uncompleted invitation requires replacement.

#### Scenario: Partner invitation
- **WHEN** a new user follows a valid partner invitation
- **THEN** they can register and explicitly confirm pairing with its sender; an already paired sender makes the invitation invalid.

### Requirement: Owner access invitation button

The owner SHALL also have a visible bot button to create an access invitation for a new user, distinct from a partner invitation. The server SHALL reject this operation for non-owner users even if they send its command directly.

#### Scenario: A member requests an owner invitation
- **WHEN** a non-owner sends the owner invitation command
- **THEN** no access invitation is created.

### Requirement: Profile and pairing
The system SHALL collect personal city, notification name and confirmed personal timezone during onboarding. Pair creation SHALL require recipient confirmation and use invitation links exclusively; friendship codes SHALL NOT be displayed or accepted. A user SHALL belong to at most one active pair.

#### Scenario: Partner finishes registration
- **WHEN** the recipient of a partner invitation confirms their completed profile
- **THEN** one durable notification informs its sender that registration is complete, independently from the later pairing confirmation; repeated profile saves do not duplicate it, revoked invitations suppress delivery, and quiet hours apply.

#### Scenario: Competing invitations
- **WHEN** two pairing acceptances race for the same user
- **THEN** only one active pair is created and the other acceptance reports the changed state.

### Requirement: Pair location
The system SHALL require the other partner to confirm a proposed pair city and timezone. Searches SHALL default to the confirmed pair city, allow a temporary city override, and ask for a search city while the pair city remains unconfirmed.

#### Scenario: Trip search
- **WHEN** a user requests places in another city
- **THEN** that search uses the requested city without changing either personal profile or the confirmed pair city.

### Requirement: Personal preferences
The system SHALL support optional personal interests, unwanted activities, food and accessibility restrictions and transport preferences. Persistent preferences inferred from conversation SHALL require explicit confirmation.

#### Scenario: Unset profile
- **WHEN** preferences or geolocation are absent
- **THEN** the user can use the bot without supplying them and the bot does not invent restrictions.

### Requirement: Separation and personal archive
The system SHALL close mutual access and cancel future pair notifications immediately on pair termination. Each user SHALL retain their own ideas and reviews in a personal archive; shared plans and partner reviews SHALL not transfer to a new pair.

#### Scenario: Solo collection
- **WHEN** a registered user without a pair adds ideas and later forms a pair
- **THEN** those personal ideas can join the new common collection while another former partner's data remains inaccessible.

### Requirement: Export and account deletion
The system SHALL provide personal data export and explicitly confirmed account deletion. Deletion SHALL end the pair, remove personal conversations, preferences and reviews, anonymize retained common cards, preserve the partner's own data, and survive backup restoration.

#### Scenario: Backup after deletion
- **WHEN** a backup predating deletion is restored
- **THEN** recorded deletions are reapplied before user access; retained backups expire within the agreed four-week rotation.
