## Purpose

Сделать обычную переписку главным способом поиска и планирования, сохранив проверяемые результаты, личную приватность и явное сохранение изменений.

## ADDED Requirements

### Requirement: Conversational primary interface
The system SHALL use DeepSeek for ordinary-text requests without required commands, retrieve current authorized collection data automatically and support contextual followup constraints. Buttons and manual editing SHALL remain available as secondary interaction.

#### Scenario: Refining suggestions
- **WHEN** the user follows a suggestion with «без кино и ближе к центру»
- **THEN** the new selection uses the current conversation and revised conditions without asking for already known data.

### Requirement: Explicit constraints
Explicit non-budget request constraints SHALL be mandatory in the prototype; budget calculations are deferred. Unset constraints SHALL not be imposed. Unknown price, distance or hours SHALL not be represented as confirmed compliance; only missing essential action details SHALL be clarified.

#### Scenario: Budget ceiling
- **WHEN** the user sets a ceiling and a place has no verified price
- **THEN** the prototype explains that budget planning is deferred and SHALL NOT promise the total fits; it does not ask for a budget.

### Requirement: Collection default and eligibility
Automatic suggestions SHALL default to the collection, prioritize mutual interest and exclude archived, frozen or personally refused places. Unevaluated places SHALL remain eligible with unknown interest labeled. Empty matches SHALL explain why and offer changed conditions, manual frozen choices or explicit internet search.

#### Scenario: Everything frozen
- **WHEN** no eligible collection place matches
- **THEN** the bot offers next actions and does not silently unfreeze places or call internet search.

### Requirement: Explicit web search
Internet search SHALL run only after an explicit request or search button. Results SHALL offer three to five grounded cards when enough results exist, explain relevance, show sources and unknown data, and provide save-to-collection and more-results actions without automatic saving.

#### Scenario: Fewer trustworthy results
- **WHEN** search supports only two places
- **THEN** the bot shows two with a limitation explanation instead of inventing additional establishments.

### Requirement: Confirmed mutations
AI SHALL prepare versioned drafts for collection, plan, review and persistent-preference changes. A user action SHALL commit changes; model output alone SHALL not authorize writes. Sensitive deletion SHALL require its separate explicit confirmation.

#### Scenario: Search-page instruction
- **WHEN** retrieved content tells the model to change data or reveal secrets
- **THEN** it is treated as source data, cannot expand permissions, and cannot commit a mutation.

### Requirement: Scoped data and consent
Pair-data use SHALL require both partners' AI consent. DeepSeek SHALL receive only needed place fields, plan dates, permitted preferences and the caller's conversation. Telegram identifiers, notification names and photos SHALL be excluded; private notes and review text SHALL require separate permission.

#### Scenario: Consent absent or withdrawn
- **WHEN** either partner lacks pair AI consent
- **THEN** pair-data requests are not sent and manual collection and planning remain usable.

#### Scenario: Reopening an enabled assistant
- **WHEN** a user who enabled the assistant opens its menu again, including after application restart
- **THEN** the UI shows stored consent as enabled without creating another consent draft, distinguishes missing partner consent, and offers large navigation and settings buttons; commands remain optional.

### Requirement: Private conversation retention
Each user SHALL have a private contextual conversation retained for thirty days, with clear-history and new-conversation actions. Confirmed structured data SHALL remain separate. Partners and the owner panel SHALL not expose those personal texts.

#### Scenario: Clear history
- **WHEN** a user clears their AI history
- **THEN** conversation context is removed while saved plans, places and confirmed preferences remain.

### Requirement: Cost visibility and errors
AI and search SHALL initially have no application request quotas or hard spending cutoff. The prototype SHALL track usage and uncertainty only; monetary targets, cost dashboards and quota configuration are deferred. Failed paid calls SHALL repeat only after an explicit user retry.

#### Scenario: Target reached
- **WHEN** a monthly monetary target would have been reached
- **THEN** no budget-target notification or spending cutoff is enabled in the prototype.

#### Scenario: Provider failure
- **WHEN** DeepSeek or search fails
- **THEN** the bot preserves the request and draft, offers «Повторить», makes no automatic paid retry and keeps manual features usable.

#### Scenario: Personal chat while partner consent is missing
- **WHEN** the caller has enabled AI but their active partner has not
- **THEN** personal text chat and new conversation remain available, using only the caller's messages, personal city and timezone; shared read/edit tools are unavailable, while explicit external search, new-card creation and confirmation-based saving into the shared collection remain available. A new date draft without existing place IDs may be proposed, and conversations containing shared pair context are not reused. Withdrawal of the caller's own consent blocks provider calls.
