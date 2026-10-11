## Purpose

Позволить управлять ботом голосовыми сообщениями с теми же правилами приватности, уточнения и подтверждения изменений, что и в текстовом диалоге.

Status: Deferred beyond the 2026-10-12 prototype. Voice messages receive a text-input explanation; the following requirements apply to a future release.

## ADDED Requirements

### Requirement: Voice conversation
The system SHALL transcribe Telegram voice messages locally with Whisper tiny, show the transcription and require «Верно» before processing through the contextual AI flow. The user SHALL be able to correct the interpreted text. Uncertain or ambiguous requests SHALL be clarified.

#### Scenario: Unclear date
- **WHEN** recognition cannot reliably distinguish the requested day or venue
- **THEN** the bot asks for clarification before preparing an action based on that ambiguous field.

#### Scenario: Ordinary voice request
- **WHEN** a transcription is ready
- **THEN** it is shown with confirmation and correction actions; no AI processing occurs before confirmation.

### Requirement: Equal mutation boundaries
Voice input SHALL not bypass draft saving or sensitive-action confirmation. A speech-recognition failure SHALL offer retry or text input without corrupting an existing draft.

#### Scenario: Voice deletion request
- **WHEN** a voice message requests account deletion
- **THEN** the separate explicit account-deletion confirmation is still required.

### Requirement: Audio privacy
Downloaded audio SHALL be temporary and deleted after recognition or failure. Only transcription text SHALL enter the personal thirty-day conversation history. External transcription SHALL require consent before audio is transmitted to its provider.

#### Scenario: Local transcription
- **WHEN** audio is recognized locally
- **THEN** no audio is sent to DeepSeek; only permitted resulting text enters the usual AI flow.
