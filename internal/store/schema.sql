CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    code TEXT NOT NULL UNIQUE,
    role TEXT NOT NULL DEFAULT 'member' CHECK(role IN ('owner','member')),
    status TEXT NOT NULL DEFAULT 'onboarding' CHECK(status IN ('onboarding','ready','deleted')),
    city TEXT,
    timezone TEXT,
    notification_name TEXT,
    partner_nickname TEXT,
    quiet_start TEXT NOT NULL DEFAULT '23:00',
    quiet_end TEXT NOT NULL DEFAULT '09:00',
    preferences TEXT NOT NULL DEFAULT '{}',
    reminder_enabled INTEGER NOT NULL DEFAULT 1 CHECK(reminder_enabled IN (0,1)),
    created_at REAL NOT NULL
);
CREATE TABLE pairs (
    id INTEGER PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','ended')),
    city TEXT,
    timezone TEXT,
    proposed_city TEXT,
    proposed_timezone TEXT,
    proposed_by INTEGER REFERENCES users(id),
    location_version INTEGER NOT NULL DEFAULT 0,
    feedback_time TEXT NOT NULL DEFAULT '12:00',
    created_at REAL NOT NULL,
    ended_at REAL
);
CREATE TABLE pair_members (
    pair_id INTEGER NOT NULL REFERENCES pairs(id),
    user_id INTEGER NOT NULL REFERENCES users(id),
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
    PRIMARY KEY(pair_id,user_id)
);
CREATE UNIQUE INDEX one_active_pair ON pair_members(user_id) WHERE active=1;
CREATE TABLE invitations (
    token_hash TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK(kind IN ('access','partner','owner')),
    sender_id INTEGER REFERENCES users(id),
    claimed_by INTEGER REFERENCES users(id),
    created_at REAL NOT NULL,
    expires_at REAL NOT NULL,
    revoked_at REAL,
    consumed_at REAL
);
CREATE TABLE consents (
    user_id INTEGER NOT NULL REFERENCES users(id),
    kind TEXT NOT NULL CHECK(kind IN ('ai','notes','reviews','external_audio')),
    enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
    updated_at REAL NOT NULL,
    PRIMARY KEY(user_id,kind)
);
CREATE TABLE places (
    id INTEGER PRIMARY KEY,
    author_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    personal_user_id INTEGER REFERENCES users(id),
    pair_id INTEGER REFERENCES pairs(id),
    title TEXT NOT NULL,
    address TEXT,
    city TEXT,
    map_url TEXT,
    description TEXT NOT NULL DEFAULT '',
    price REAL CHECK(price IS NULL OR price >= 0),
    price_basis TEXT CHECK(price_basis IS NULL OR price_basis IN ('person','couple')),
    sources TEXT NOT NULL DEFAULT '[]',
    photos TEXT NOT NULL DEFAULT '[]',
    verified_at REAL,
    archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
    version INTEGER NOT NULL DEFAULT 1,
    created_at REAL NOT NULL,
    CHECK((personal_user_id IS NULL) != (pair_id IS NULL))
);
CREATE INDEX places_pair ON places(pair_id,archived);
CREATE TABLE categories (
    id INTEGER PRIMARY KEY,
    scope TEXT NOT NULL,
    title TEXT NOT NULL,
    UNIQUE(scope,title)
);
CREATE TABLE place_categories (
    place_id INTEGER NOT NULL REFERENCES places(id) ON DELETE CASCADE,
    category_id INTEGER NOT NULL REFERENCES categories(id),
    PRIMARY KEY(place_id,category_id)
);
CREATE TABLE place_preferences (
    place_id INTEGER NOT NULL REFERENCES places(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id),
    interest TEXT CHECK(interest IS NULL OR interest IN ('want','uninterested')),
    refused INTEGER NOT NULL DEFAULT 0 CHECK(refused IN (0,1)),
    PRIMARY KEY(place_id,user_id)
);
CREATE TABLE plans (
    id INTEGER PRIMARY KEY,
    pair_id INTEGER NOT NULL REFERENCES pairs(id),
    author_id INTEGER NOT NULL REFERENCES users(id),
    title TEXT NOT NULL,
    start_at REAL NOT NULL,
    end_at REAL NOT NULL CHECK(end_at > start_at),
    timezone TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'planned' CHECK(status IN ('planned','cancelled','visited','not_went')),
    version INTEGER NOT NULL DEFAULT 1,
    created_at REAL NOT NULL
);
CREATE TABLE plan_items (
    plan_id INTEGER NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    place_id INTEGER NOT NULL REFERENCES places(id),
    position INTEGER NOT NULL,
    snapshot TEXT NOT NULL,
    PRIMARY KEY(plan_id,position)
);
CREATE TABLE candidates (
    pair_id INTEGER NOT NULL REFERENCES pairs(id),
    place_id INTEGER NOT NULL REFERENCES places(id) ON DELETE CASCADE,
    added_by INTEGER NOT NULL REFERENCES users(id),
    position INTEGER NOT NULL,
    PRIMARY KEY(pair_id,place_id)
);
CREATE TABLE visits (
    id INTEGER PRIMARY KEY,
    pair_id INTEGER REFERENCES pairs(id),
    personal_user_id INTEGER REFERENCES users(id),
    plan_id INTEGER REFERENCES plans(id),
    marked_by INTEGER NOT NULL REFERENCES users(id),
    visit_date TEXT NOT NULL,
    date_precision TEXT NOT NULL DEFAULT 'day' CHECK(date_precision IN ('day','month','year')),
    visited_at REAL,
    valid INTEGER NOT NULL DEFAULT 1 CHECK(valid IN (0,1)),
    automatic_freeze INTEGER NOT NULL DEFAULT 1 CHECK(automatic_freeze IN (0,1)),
    created_at REAL NOT NULL
);
CREATE TABLE visit_items (
    visit_id INTEGER NOT NULL REFERENCES visits(id),
    place_id INTEGER NOT NULL REFERENCES places(id),
    PRIMARY KEY(visit_id,place_id)
);
CREATE TABLE reviews (
    visit_id INTEGER NOT NULL REFERENCES visits(id),
    place_id INTEGER NOT NULL REFERENCES places(id),
    user_id INTEGER NOT NULL REFERENCES users(id),
    rating TEXT CHECK(rating IS NULL OR rating IN ('liked','neutral','disliked')),
    again TEXT CHECK(again IS NULL OR again IN ('again','not_soon','never')),
    comment TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(visit_id,place_id,user_id),
    FOREIGN KEY(visit_id,place_id) REFERENCES visit_items(visit_id,place_id)
);
CREATE TABLE freeze_overrides (
    place_id INTEGER PRIMARY KEY REFERENCES places(id),
    until_at REAL,
    changed_by INTEGER NOT NULL REFERENCES users(id),
    changed_at REAL NOT NULL
);
CREATE TABLE conversations (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id),
    created_at REAL NOT NULL
);
CREATE TABLE messages (
    id INTEGER PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK(role IN ('user','assistant')),
    text TEXT NOT NULL,
    created_at REAL NOT NULL
);
CREATE INDEX message_retention ON messages(created_at);
CREATE TABLE drafts (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id),
    pair_id INTEGER REFERENCES pairs(id),
    kind TEXT NOT NULL,
    payload TEXT NOT NULL,
    target_version INTEGER,
    created_at REAL NOT NULL,
    expires_at REAL NOT NULL,
    applied_at REAL,
    result TEXT
);
CREATE TABLE notifications (
    id INTEGER PRIMARY KEY,
    semantic_key TEXT NOT NULL UNIQUE,
    pair_id INTEGER REFERENCES pairs(id),
    recipient_id INTEGER NOT NULL REFERENCES users(id),
    kind TEXT NOT NULL,
    payload TEXT NOT NULL,
    available_at REAL NOT NULL,
    expires_at REAL NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','sending','sent','cancelled','failed','uncertain')),
    attempts INTEGER NOT NULL DEFAULT 0,
    telegram_message_id INTEGER,
    error TEXT,
    created_at REAL NOT NULL
);
CREATE INDEX due_notifications ON notifications(status,available_at);
CREATE TABLE api_usage (
    id INTEGER PRIMARY KEY,
    user_id INTEGER REFERENCES users(id),
    provider TEXT NOT NULL,
    model TEXT,
    input_tokens INTEGER,
    cached_tokens INTEGER,
    output_tokens INTEGER,
    search_requests INTEGER,
    estimated_usd REAL,
    estimated_rub REAL,
    uncertain INTEGER NOT NULL DEFAULT 0,
    success INTEGER NOT NULL,
    created_at REAL NOT NULL
);
CREATE TABLE backup_runs (
    id INTEGER PRIMARY KEY,
    started_at REAL NOT NULL,
    finished_at REAL,
    status TEXT NOT NULL,
    path TEXT,
    error TEXT
);
CREATE TABLE cooldowns (
    user_id INTEGER NOT NULL REFERENCES users(id),
    kind TEXT NOT NULL,
    at REAL NOT NULL,
    last_phrase INTEGER,
    PRIMARY KEY(user_id,kind)
);
CREATE TABLE deletion_ledger (
    user_hash TEXT PRIMARY KEY,
    deleted_at REAL NOT NULL
);
