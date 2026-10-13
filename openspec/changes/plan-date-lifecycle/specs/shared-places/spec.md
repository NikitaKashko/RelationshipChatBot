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

### Requirement: Interest state actions
The author of a newly saved card SHALL start with «Хочу сходить». Its confirmation SHALL show that state and omit redundant interest and immediate archive actions. A normally opened card SHALL offer only actions that change the viewer's current state; an unrated partner SHALL see «Тоже хочу» when the author wants to visit.

#### Scenario: Author saves a new place
- **WHEN** a user confirms adding a new place to the collection
- **THEN** the returned card says that they want to visit, offers adding it to a date draft, and does not offer the same interest action or immediate archival.

### Requirement: Pair collection merge
When two users form a pair, their non-archived personal place collections SHALL be merged into one shared collection while preserving each original author. New places added by either active partner SHALL enter that collection.

#### Scenario: Pair collection merge
- **WHEN** two users with personal ideas create a pair
- **THEN** both sets of ideas appear in one shared collection with their original authors and without duplicate ownership transfer.

### Requirement: Editing and archival
Both partners SHALL be able to edit shared card fields, archive cards and restore them. Original authorship and visit history SHALL survive archival; personal opinions and reviews SHALL be editable only by their author.

The Telegram card SHALL expose a visible edit action. Manual editing SHALL preload the current values, allow keeping each value without retyping it, allow clearing optional text fields, and prepare a version-checked preview before saving.

#### Scenario: Archived planned place
- **WHEN** a card referenced in an old plan is archived
- **THEN** the old plan retains its reference and historical description while automatic recommendations exclude the archived card.

#### Scenario: Partner edits a shared card
- **WHEN** either partner opens a card, changes its title or optional details and confirms the preview
- **THEN** the current card is updated, its original author, individual interests, photos, sources and visit history remain intact, and a concurrent newer edit is not overwritten.

### Requirement: Saved search presentation
After a searched place is saved, the bot SHALL visibly confirm collection entry and present the actions allowed for its current state. Collection cards SHALL omit unavailable optional fields and legacy unknown-field annotations.

#### Scenario: Saved search card
- **WHEN** a user presses add-to-collection on a reviewed search result
- **THEN** the returned card starts with a successful collection confirmation, shows clearly named actions and omits unknown address, budget, hours and legacy «Не подтверждено» lines.
