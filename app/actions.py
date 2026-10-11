"""Durable explicit-confirmation actions; the model only prepares drafts."""

import json
import secrets

from app.identity import IdentityService
from app.storage import AccessDenied, Conflict


class Actions:
    def __init__(self, db):
        self.db = db
        self.identity = IdentityService(db)
        self.handlers = {
            'profile': self.identity.finish_profile,
            'consent': self.identity.set_consent,
            'quiet_hours': self.identity.set_quiet_hours,
            'location_proposal': self.identity.propose_location,
            'location_confirm': self.identity.confirm_location,
        }

    def prepare(self, actor, kind, payload, *, target_version=None):
        if kind not in self.handlers:
            raise ValueError('Неизвестное действие.')
        self.db.user(actor, allow_onboarding=kind == 'profile')
        if not isinstance(payload, dict) or any(k in payload for k in ('actor', 'actor_id', 'user_id', 'pair_id')):
            raise AccessDenied('Пользователь и пара определяются сервером.')
        pair = self.db.pair(actor)
        draft_id = secrets.token_urlsafe(18)
        now = self.db.clock()
        self.db.conn.execute('INSERT INTO drafts(id,user_id,pair_id,kind,payload,target_version,created_at,expires_at) '
                             'VALUES(?,?,?,?,?,?,?,?)',
                             (draft_id, actor, pair['id'] if pair else None, kind, json.dumps(payload, ensure_ascii=False),
                              target_version, now, now+86400))
        return draft_id

    def apply(self, actor, draft_id):
        with self.db.transaction():
            draft = self.db.one('SELECT * FROM drafts WHERE id=? AND user_id=?', (draft_id, actor))
            if not draft:
                raise AccessDenied('Этот черновик недоступен.')
            self.db.user(actor, allow_onboarding=draft['kind'] == 'profile')
            if draft['expires_at'] <= self.db.clock():
                raise Conflict('Черновик устарел. Подготовь изменение заново.')
            pair = self.db.pair(actor)
            if draft['pair_id'] != (pair['id'] if pair else None):
                raise Conflict('Пара изменилась. Подготовь изменение заново.')
            if draft['applied_at'] is not None:
                return json.loads(draft['result'])
            handler = self.handlers.get(draft['kind'])
            if not handler:
                raise ValueError('Действие больше не поддерживается.')
            result = handler(actor, **json.loads(draft['payload']))
            self.db.conn.execute('UPDATE drafts SET applied_at=?,result=? WHERE id=?',
                                 (self.db.clock(), json.dumps(result, ensure_ascii=False), draft_id))
            return result
