"""Identity actions shared by Telegram buttons and AI.

Actor IDs come from authenticated Telegram updates, never model arguments.
"""

import hashlib
import re
import secrets
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from app.storage import AccessDenied, Conflict


def token_hash(token):
    return hashlib.sha256(token.encode('utf-8')).hexdigest()


class IdentityService:
    def __init__(self, db):
        self.db = db

    def bootstrap_invitation(self):
        """Local CLI only. A single owner invitation bootstraps the install."""
        with self.db.transaction():
            if self.db.one("SELECT id FROM users WHERE role='owner' AND status<>'deleted'"):
                raise Conflict('Владелец уже зарегистрирован.')
            self.db.conn.execute("UPDATE invitations SET revoked_at=? WHERE kind='owner' AND consumed_at IS NULL",
                                 (self.db.clock(),))
            return self._issue(None, 'owner')

    def _issue(self, sender, kind):
        token = secrets.token_urlsafe(24)
        now = self.db.clock()
        self.db.conn.execute(
            'INSERT INTO invitations(token_hash,kind,sender_id,created_at,expires_at) VALUES(?,?,?,?,?)',
            (token_hash(token), kind, sender, now, now + 7*86400),
        )
        return token

    def issue(self, actor, kind):
        with self.db.transaction():
            user = self.db.user(actor)
            if kind == 'access':
                if user['role'] != 'owner':
                    raise AccessDenied('Приглашения доступа создаёт владелец.')
            elif kind == 'partner':
                if self.db.pair(actor):
                    raise Conflict('Ты уже в паре.')
            else:
                raise ValueError('Неизвестный вид приглашения.')
            return self._issue(actor, kind)

    def revoke(self, actor, token):
        with self.db.transaction():
            user = self.db.user(actor)
            invitation = self.db.one('SELECT * FROM invitations WHERE token_hash=?', (token_hash(token),))
            if not invitation or not (invitation['sender_id'] == actor or user['role'] == 'owner'):
                raise AccessDenied('Это приглашение недоступно.')
            self.db.conn.execute('UPDATE invitations SET revoked_at=? WHERE token_hash=?',
                                 (self.db.clock(), token_hash(token)))

    def _valid(self, token, actor=None):
        invitation = self.db.one('SELECT * FROM invitations WHERE token_hash=?', (token_hash(token),))
        if not invitation or invitation['revoked_at'] is not None or invitation['consumed_at'] is not None:
            raise Conflict('Приглашение уже использовано или отменено.')
        if invitation['expires_at'] <= self.db.clock():
            raise Conflict('Срок приглашения истёк. Попроси новую ссылку.')
        if invitation['kind'] == 'partner':
            self.db.user(invitation['sender_id'])
            if self.db.pair(invitation['sender_id']):
                raise Conflict('Отправитель уже в паре.')
        if actor is not None and invitation['claimed_by'] not in (None, actor):
            raise AccessDenied('Эта ссылка закреплена за другим аккаунтом.')
        if actor is not None and invitation['sender_id'] == actor:
            raise Conflict('Нельзя пригласить себя в пару.')
        return invitation

    def claim(self, actor, name, token):
        with self.db.transaction():
            invitation = self._valid(token, actor)
            if invitation['kind'] == 'partner' and self.db.pair(actor):
                raise Conflict('Ты уже в паре.')
            existing = self.db.one('SELECT * FROM users WHERE id=?', (actor,))
            if existing and existing['status'] == 'deleted':
                raise Conflict('Для повторной регистрации требуется новое создание профиля владельцем.')
            if not existing:
                self.db.conn.execute(
                    'INSERT INTO users(id,name,code,notification_name,role,created_at) VALUES(?,?,?,?,?,?)',
                    (actor, name, secrets.token_hex(5).upper(), name,
                     'owner' if invitation['kind'] == 'owner' else 'member', self.db.clock()),
                )
            elif invitation['kind'] == 'owner':
                if self.db.one("SELECT id FROM users WHERE role='owner' AND id<>? AND status<>'deleted'", (actor,)):
                    raise Conflict('Владелец уже существует.')
                self.db.conn.execute("UPDATE users SET role='owner' WHERE id=?", (actor,))
            self.db.conn.execute('UPDATE invitations SET claimed_by=? WHERE token_hash=?',
                                 (actor, invitation['token_hash']))
            return invitation['kind']

    def resume(self, actor):
        self.db.user(actor, allow_onboarding=True)
        return self.db.all('SELECT kind,sender_id,expires_at FROM invitations WHERE claimed_by=? '
                           'AND revoked_at IS NULL AND consumed_at IS NULL AND expires_at>?',
                           (actor, self.db.clock()))

    def finish_profile(self, actor, *, city, timezone, name=None):
        if not city or not city.strip():
            raise ValueError('Укажи свой город.')
        try:
            ZoneInfo(timezone)
        except (ZoneInfoNotFoundError, ValueError):
            raise ValueError('Неизвестный часовой пояс.') from None
        with self.db.transaction():
            user = self.db.user(actor, allow_onboarding=True)
            pending = self.resume(actor)
            if user['status'] != 'ready' and not pending:
                # Migrated prototype users have no invitation; their existing
                # enrollment is retained but they must finish the new profile.
                legacy = self.db.one("SELECT name FROM sqlite_master WHERE name='legacy_users'")
                if not legacy or not self.db.one('SELECT id FROM legacy_users WHERE id=?', (actor,)):
                    raise AccessDenied('Нужно действующее приглашение.')
            self.db.conn.execute("UPDATE users SET city=?,timezone=?,notification_name=?,status='ready' WHERE id=?",
                                 (city.strip(), timezone, (name or user['notification_name'] or user['name']).strip(), actor))
            self.db.conn.execute("UPDATE invitations SET consumed_at=? WHERE claimed_by=? AND kind IN ('access','owner') "
                                 'AND revoked_at IS NULL AND consumed_at IS NULL AND expires_at>?',
                                 (self.db.clock(), actor, self.db.clock()))

    def accept_pair(self, actor, token):
        """Recipient's explicit confirmation; serialized against other accepts."""
        with self.db.transaction():
            self.db.user(actor)
            invitation = self._valid(token, actor)
            if invitation['kind'] != 'partner':
                raise ValueError('Это не приглашение в пару.')
            if invitation['claimed_by'] != actor:
                raise AccessDenied('Сначала открой своё приглашение.')
            sender = invitation['sender_id']
            self.db.user(sender)
            if self.db.pair(actor) or self.db.pair(sender):
                raise Conflict('Один из вас уже в паре.')
            pair_id = self.db.conn.execute('INSERT INTO pairs(created_at) VALUES(?)', (self.db.clock(),)).lastrowid
            for uid in (actor, sender):
                self.db.conn.execute('INSERT INTO pair_members(pair_id,user_id) VALUES(?,?)', (pair_id, uid))
                self.db.conn.execute('UPDATE places SET pair_id=?,personal_user_id=NULL WHERE personal_user_id=? AND archived=0',
                                     (pair_id, uid))
            self.db.conn.execute('UPDATE invitations SET consumed_at=? WHERE token_hash=?',
                                 (self.db.clock(), invitation['token_hash']))
            self.db.conn.execute("UPDATE invitations SET revoked_at=? WHERE kind='partner' AND consumed_at IS NULL "
                                 'AND (sender_id IN (?,?) OR claimed_by IN (?,?))',
                                 (self.db.clock(), actor, sender, actor, sender))
            self.db.enqueue(sender, 'pair_created', {'partner_name': self.db.user(actor)['notification_name']},
                            key=f'pair:{pair_id}:created', pair_id=pair_id)
            return pair_id

    def request_by_code(self, actor, code):
        with self.db.transaction():
            self.db.user(actor)
            other = self.db.one("SELECT * FROM users WHERE code=? AND status='ready'", (code.strip().upper(),))
            if not other or other['id'] == actor or self.db.pair(actor) or self.db.pair(other['id']):
                raise Conflict('Код недоступен или один из вас уже в паре.')
            token = self._issue(actor, 'partner')
            self.db.conn.execute('UPDATE invitations SET claimed_by=? WHERE token_hash=?',
                                 (other['id'], token_hash(token)))
            self.db.enqueue(other['id'], 'pair_invitation', {'token': token, 'sender_name': self.db.user(actor)['notification_name']},
                            key=f'invite:{token_hash(token)}', expires_at=self.db.clock()+7*86400)
            return other['id']

    def propose_location(self, actor, city, timezone):
        if not city or not city.strip():
            raise ValueError('Укажи город пары.')
        try:
            ZoneInfo(timezone)
        except (ZoneInfoNotFoundError, ValueError):
            raise ValueError('Неизвестный часовой пояс.') from None
        with self.db.transaction():
            pair = self.db.require_pair(actor)
            version = pair['location_version'] + 1
            self.db.conn.execute('UPDATE pairs SET proposed_city=?,proposed_timezone=?,proposed_by=?,location_version=? WHERE id=?',
                                 (city.strip(), timezone, actor, version, pair['id']))
            self.db.enqueue(self.db.partner_id(actor), 'location_proposal',
                            {'city': city.strip(), 'timezone': timezone, 'version': version},
                            key=f'pair:{pair["id"]}:location:{version}', pair_id=pair['id'])
            return version

    def confirm_location(self, actor, version):
        with self.db.transaction():
            pair = self.db.require_pair(actor)
            if pair['proposed_by'] is None or pair['proposed_by'] == actor:
                raise AccessDenied('Город подтверждает второй партнёр.')
            if pair['location_version'] != version:
                raise Conflict('Город уже изменился. Открой новое предложение.')
            self.db.conn.execute('UPDATE pairs SET city=proposed_city,timezone=proposed_timezone,'
                                 'proposed_city=NULL,proposed_timezone=NULL,proposed_by=NULL WHERE id=?', (pair['id'],))
            return self.db.pair(actor)

    def search_city(self, actor, override=None):
        self.db.user(actor)
        pair = self.db.pair(actor)
        return override or (pair['city'] if pair else None)

    def set_quiet_hours(self, actor, start, end):
        for value in (start, end):
            if not re.fullmatch(r'(?:[01]\d|2[0-3]):[0-5]\d', value):
                raise ValueError('Время должно быть в формате ЧЧ:ММ.')
        self.db.user(actor)
        self.db.conn.execute('UPDATE users SET quiet_start=?,quiet_end=? WHERE id=?', (start, end, actor))

    def set_consent(self, actor, kind, enabled):
        if kind not in ('ai', 'notes', 'reviews', 'external_audio'):
            raise ValueError('Неизвестное согласие.')
        with self.db.transaction():
            self.db.user(actor)
            self.db.conn.execute('INSERT INTO consents VALUES(?,?,?,?) ON CONFLICT(user_id,kind) '
                                 'DO UPDATE SET enabled=excluded.enabled,updated_at=excluded.updated_at',
                                 (actor, kind, int(bool(enabled)), self.db.clock()))
            if not enabled:
                self.db.conn.execute("DELETE FROM drafts WHERE kind LIKE 'ai_%' AND applied_at IS NULL AND "
                                     '(user_id=? OR pair_id=(SELECT pair_id FROM pair_members WHERE user_id=? AND active=1))',
                                     (actor, actor))
