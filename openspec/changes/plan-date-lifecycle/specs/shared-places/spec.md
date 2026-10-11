## Purpose

Вести общую коллекцию идей с понятными авторами, источниками и предпочтениями, без обязательной оценки каждой новой идеи вторым партнёром.

## ADDED Requirements

### Requirement: Verified save flow
The system SHALL accept map links and ordinary text, prepare a card for review, and save only after a user action. It SHALL allow original ideas without an address and clarify only missing data necessary for saving.

#### Scenario: Addressless idea
- **WHEN** a user adds «пикник у озера» without an address
- **THEN** the bot offers a card with title and category and does not require fabricated coordinates.

### Requirement: Card content
Cards SHALL contain a title, one or more categories and addition author. Address, Yandex Maps link, description, price basis and photos SHALL be optional. Retrieved facts SHALL record their source and verification date; unknown facts SHALL be labeled rather than invented.

#### Scenario: Unverified price
- **WHEN** an otherwise useful place lacks confirmed prices or hours
- **THEN** the card identifies the missing information and can be saved as an unverified idea with its source; prices specify per-person or per-couple when known.

### Requirement: Photo presentation
The system SHALL display one cover and up to two additional photos on demand only when use is permitted, accept user photos, and support text cards without photos. Unrelated images SHALL not be presented as verified place photos.

#### Scenario: No permitted image
- **WHEN** no usable place photo exists
- **THEN** the bot displays a text card with available facts and links.

### Requirement: Categories and duplicates
The system SHALL provide starter and user-created categories and support several categories per place. Matching map links or matching name and address SHALL prompt use of an existing card; separate branches SHALL remain separate places.

#### Scenario: Second addition
- **WHEN** the second partner adds an existing place
- **THEN** the existing card is proposed, its original author remains, and the second partner can record their own interest.

### Requirement: Optional interest and filters
The system SHALL support voluntary «Хочу сходить / Не интересно» and filters for all, own additions, partner additions, mutual interest, frozen and visited places. New additions SHALL not require partner votes or generate review-request notifications.

#### Scenario: Unevaluated idea
- **WHEN** only one partner has expressed interest
- **THEN** the place remains available for AI with unknown partner interest labeled; it is neither a mutual match nor an automatic refusal.

### Requirement: Editing and archival
Both partners SHALL be able to edit shared card fields, archive cards and restore them. Original authorship and visit history SHALL survive archival; personal opinions and reviews SHALL be editable only by their author.

#### Scenario: Archived planned place
- **WHEN** a card referenced in an old plan is archived
- **THEN** the old plan retains its reference and historical description while automatic recommendations exclude the archived card.
