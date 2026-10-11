"""Versioned SQLite storage. Network calls never belong inside a transaction.

This is the release storage; app.db remains the legacy prototype until the new
Telegram router is switched over. Never run the legacy router on a migrated DB.
"""

from contextlib import closing, contextmanager
import secrets
import sqlite3
import time
from pathlib import Path


class AccessDenied(ValueError):
    pass


class Conflict(ValueError):
    pass


class Database:
    VERSION = 1

    def __init__(self, path, *, clock=time.time):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.clock = clock
        self.conn = sqlite3.connect(self.path, isolation_level=None, timeout=10)
        self.conn.row_factory = sqlite3.Row
        self.conn.execute('PRAGMA foreign_keys=ON')
        self.conn.execute('PRAGMA journal_mode=WAL')
        try:
            self._migrate()
        except BaseException:
            self.conn.close()
            raise

    def close(self):
        self.conn.close()

    @contextmanager
    def transaction(self):
        """Serialize writers; nested domain actions use savepoints."""
        nested = self.conn.in_transaction
        savepoint = 's_' + secrets.token_hex(8)
        self.conn.execute(f'SAVEPOINT {savepoint}' if nested else 'BEGIN IMMEDIATE')
        try:
            yield
        except BaseException:
            if nested:
                self.conn.execute(f'ROLLBACK TO {savepoint}')
                self.conn.execute(f'RELEASE {savepoint}')
            else:
                self.conn.execute('ROLLBACK')
            raise
        else:
            self.conn.execute(f'RELEASE {savepoint}' if nested else 'COMMIT')

    def one(self, sql, params=()):
        row = self.conn.execute(sql, params).fetchone()
        return dict(row) if row else None

    def all(self, sql, params=()):
        return [dict(row) for row in self.conn.execute(sql, params).fetchall()]

    def _migrate(self):
        version = self.conn.execute('PRAGMA user_version').fetchone()[0]
        if version > self.VERSION:
            raise ValueError('База новее этой версии приложения. Откат без миграции запрещён.')
        if version == self.VERSION:
            return
        tables = {r[0] for r in self.conn.execute("SELECT name FROM sqlite_master WHERE type='table'")}
        legacy = 'users' in tables and 'partner' in {
            r[1] for r in self.conn.execute('PRAGMA table_info(users)')
        }
        if tables and not legacy:
            raise ValueError('Неизвестная схема базы: автоматическая замена запрещена.')
        if legacy:
            backup_path = self.path.with_name(self.path.name + '.pre-v1.sqlite')
            # Never overwrite the first recovery snapshot during retries.
            if not backup_path.exists():
                with closing(sqlite3.connect(backup_path)) as backup:
                    self.conn.backup(backup)
        with self.transaction():
            if legacy:
                for name in ('users', 'requests', 'places', 'likes', 'cooldowns'):
                    if name in tables:
                        self.conn.execute(f'ALTER TABLE {name} RENAME TO legacy_{name}')
            # schema.sql contains plain DDL, no triggers or semicolons in strings.
            for statement in Path(__file__).with_name('schema.sql').read_text(encoding='utf-8').split(';'):
                if statement.strip():
                    self.conn.execute(statement)
            if legacy:
                self._import_legacy(tables)
            self.conn.execute(f'PRAGMA user_version={self.VERSION}')

    def _import_legacy(self, tables):
        now = self.clock()
        users = self.all('SELECT * FROM legacy_users')
        for user in users:
            self.conn.execute(
                'INSERT INTO users(id,name,code,status,notification_name,created_at) VALUES(?,?,?,?,?,?)',
                (user['id'], user['name'], user['code'], 'onboarding', user['name'], now),
            )
        membership = {}
        indexed = {u['id']: u for u in users}
        for user in users:
            partner = indexed.get(user['partner'])
            if partner and partner['partner'] == user['id'] and user['id'] < partner['id']:
                pair_id = self.conn.execute('INSERT INTO pairs(created_at) VALUES(?)', (now,)).lastrowid
                for uid in (user['id'], partner['id']):
                    self.conn.execute('INSERT INTO pair_members(pair_id,user_id) VALUES(?,?)', (pair_id, uid))
                    membership[uid] = pair_id
        if 'places' not in tables:
            return
        for place in self.all('SELECT * FROM legacy_places'):
            uid = place['owner']
            pair_id = membership.get(uid)
            if not pair_id or membership.get(place['partner']) != pair_id:
                pair_id = None
            self.conn.execute(
                'INSERT INTO places(id,author_id,personal_user_id,pair_id,title,description,created_at) VALUES(?,?,?,?,?,?,?)',
                (place['id'], uid, None if pair_id else uid, pair_id, place['title'], place['note'], now),
            )
            scope = f'pair:{pair_id}' if pair_id else f'user:{uid}'
            self.conn.execute('INSERT OR IGNORE INTO categories(scope,title) VALUES(?,?)', (scope, place['category']))
            category = self.one('SELECT id FROM categories WHERE scope=? AND title=?', (scope, place['category']))
            self.conn.execute('INSERT INTO place_categories VALUES(?,?)', (place['id'], category['id']))
        if 'likes' in tables:
            for like in self.all('SELECT * FROM legacy_likes'):
                place = self.one('SELECT * FROM places WHERE id=?', (like['place'],))
                if place and (place['personal_user_id'] == like['user'] or (
                    place['pair_id'] and membership.get(like['user']) == place['pair_id']
                )):
                    self.conn.execute('INSERT INTO place_preferences(place_id,user_id,interest) VALUES(?,?,?)',
                                      (like['place'], like['user'], 'want'))
        # Invitations and AI consent are intentionally not renewed implicitly.

    def user(self, uid, *, allow_onboarding=False):
        user = self.one('SELECT * FROM users WHERE id=?', (uid,))
        if not user or user['status'] == 'deleted' or (user['status'] != 'ready' and not allow_onboarding):
            raise AccessDenied('Сначала закончи регистрацию по приглашению.')
        return user

    def pair(self, uid):
        return self.one('SELECT p.* FROM pairs p JOIN pair_members m ON m.pair_id=p.id '
                        "WHERE m.user_id=? AND m.active=1 AND p.status='active'", (uid,))

    def require_pair(self, uid, pair_id=None):
        self.user(uid)
        pair = self.pair(uid)
        if not pair or (pair_id is not None and pair['id'] != pair_id):
            raise AccessDenied('Эта пара недоступна.')
        return pair

    def partner_id(self, uid):
        pair = self.require_pair(uid)
        row = self.one('SELECT user_id FROM pair_members WHERE pair_id=? AND active=1 AND user_id<>?',
                       (pair['id'], uid))
        if not row:
            raise AccessDenied('Партнёр недоступен.')
        return row['user_id']

    def place(self, uid, place_id):
        self.user(uid)
        place = self.one('SELECT * FROM places WHERE id=?', (place_id,))
        pair = self.pair(uid)
        if not place or not (place['personal_user_id'] == uid or (
            pair and place['pair_id'] == pair['id']
        )):
            raise AccessDenied('Это место недоступно.')
        return place

    def plan(self, uid, plan_id):
        plan = self.one('SELECT * FROM plans WHERE id=?', (plan_id,))
        if not plan:
            raise AccessDenied('Этот план недоступен.')
        self.require_pair(uid, plan['pair_id'])
        return plan

    def enqueue(self, recipient, kind, payload, *, key, pair_id=None, available_at=None, expires_at=None):
        import json
        now = self.clock()
        self.conn.execute(
            'INSERT OR IGNORE INTO notifications(semantic_key,pair_id,recipient_id,kind,payload,available_at,expires_at,created_at) '
            'VALUES(?,?,?,?,?,?,?,?)',
            (key, pair_id, recipient, kind, json.dumps(payload, ensure_ascii=False),
             now if available_at is None else available_at, now + 48*3600 if expires_at is None else expires_at, now),
        )
