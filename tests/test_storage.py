import sqlite3
import tempfile
import unittest
from pathlib import Path

from app.actions import Actions
from app.db import Store as LegacyStore
from app.identity import IdentityService, token_hash
from app.storage import AccessDenied, Conflict, Database


class ReleaseStorageTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name)/'release.sqlite'
        self.now = 1_800_000_000
        self.db = Database(self.path, clock=lambda: self.now)
        self.addCleanup(self.db.close)
        self.ids = IdentityService(self.db)
        invite = self.ids.bootstrap_invitation()
        self.ids.claim(1, 'Owner', invite)
        self.ids.finish_profile(1, city='Москва', timezone='Europe/Moscow')

    def member(self, uid):
        invite = self.ids.issue(1, 'access')
        self.ids.claim(uid, f'User {uid}', invite)
        self.ids.finish_profile(uid, city='Москва', timezone='Europe/Moscow')

    def pair(self, sender, recipient):
        invite = self.ids.issue(sender, 'partner')
        self.ids.claim(recipient, 'Member', invite)
        return self.ids.accept_pair(recipient, invite)

    def test_transaction_rollback_and_single_active_pair(self):
        self.member(2)
        self.member(3)
        pair = self.pair(1, 2)
        with self.assertRaises(sqlite3.IntegrityError):
            with self.db.transaction():
                second = self.db.conn.execute('INSERT INTO pairs(created_at) VALUES(?)', (self.now,)).lastrowid
                self.db.conn.execute('INSERT INTO pair_members VALUES(?,?,1)', (second, 1))
        self.assertEqual(len(self.db.all('SELECT * FROM pairs')), 1)
        self.assertEqual(self.db.pair(1)['id'], pair)
        with self.assertRaises(RuntimeError):
            with self.db.transaction():
                self.ids.set_consent(1, 'ai', True)
                raise RuntimeError('Simulated failure after nested action')
        self.assertIsNone(self.db.one('SELECT * FROM consents WHERE user_id=1'))

    def test_invitation_claim_resume_expiry_and_revocation(self):
        token = self.ids.issue(1, 'access')
        self.ids.claim(2, 'Bob', token)
        self.ids.claim(2, 'Bob', token)
        self.assertEqual(len(self.ids.resume(2)), 1)
        with self.assertRaises(AccessDenied):
            self.ids.claim(3, 'Charlie', token)
        self.assertIsNone(self.db.one('SELECT id FROM users WHERE id=3'))
        self.now += 7*86400
        with self.assertRaises(Conflict):
            self.ids.claim(2, 'Bob', token)
        with self.assertRaises(AccessDenied):
            self.ids.finish_profile(2, city='Москва', timezone='Europe/Moscow')
        replacement = self.ids.issue(1, 'access')
        self.ids.claim(2, 'Bob', replacement)
        self.ids.finish_profile(2, city='Москва', timezone='Europe/Moscow')
        self.assertEqual(self.db.user(2)['status'], 'ready')
        with self.assertRaises(Conflict):
            self.ids.claim(2, 'Bob', replacement)
        revoked = self.ids.issue(1, 'access')
        self.ids.revoke(1, revoked)
        with self.assertRaises(Conflict):
            self.ids.claim(3, 'Charlie', revoked)
        self.assertNotIn(token, str(self.db.all('SELECT * FROM invitations')))

    def test_competing_pair_accepts(self):
        for uid in (2, 3):
            self.member(uid)
        a = self.ids.issue(1, 'partner')
        b = self.ids.issue(3, 'partner')
        self.ids.claim(2, 'Bob', a)
        self.ids.claim(2, 'Bob', b)
        self.ids.accept_pair(2, a)
        with self.assertRaises(Conflict):
            self.ids.accept_pair(2, b)
        self.assertIsNone(self.db.pair(3))
        self.assertEqual(len(self.db.all('SELECT * FROM pairs')), 1)

    def test_pair_and_place_access_are_scoped(self):
        for uid in (2, 3, 4):
            self.member(uid)
        pair = self.pair(1, 2)
        self.pair(3, 4)
        pid = self.db.conn.execute('INSERT INTO places(author_id,pair_id,title,created_at) VALUES(?,?,?,?)',
                                   (1, pair, 'Park', self.now)).lastrowid
        self.assertEqual(self.db.place(2, pid)['title'], 'Park')
        with self.assertRaises(AccessDenied):
            self.db.place(3, pid)
        with self.assertRaises(AccessDenied):
            self.db.require_pair(4, pair)
        plan = self.db.conn.execute('INSERT INTO plans(pair_id,author_id,title,start_at,end_at,timezone,created_at) '
                                    'VALUES(?,?,?,?,?,?,?)', (pair, 1, 'Date', self.now, self.now+3600,
                                                            'Europe/Moscow', self.now)).lastrowid
        self.assertEqual(self.db.plan(2, plan)['id'], plan)
        with self.assertRaises(AccessDenied):
            self.db.plan(4, plan)

    def test_confirm_location_and_temporary_city(self):
        self.member(2)
        self.pair(1, 2)
        version = self.ids.propose_location(1, 'Москва', 'Europe/Moscow')
        with self.assertRaises(AccessDenied):
            self.ids.confirm_location(1, version)
        with self.assertRaises(Conflict):
            self.ids.confirm_location(2, version-1)
        self.ids.confirm_location(2, version)
        self.assertEqual(self.ids.search_city(1, 'Казань'), 'Казань')
        self.assertEqual(self.ids.search_city(2), 'Москва')
        self.assertEqual(self.db.user(1)['city'], 'Москва')

    def test_drafts_no_write_before_confirm_and_idempotency(self):
        actions = Actions(self.db)
        draft = actions.prepare(1, 'consent', {'kind': 'ai', 'enabled': True})
        self.assertIsNone(self.db.one('SELECT * FROM consents WHERE user_id=1'))
        with self.assertRaises(AccessDenied):
            actions.apply(2, draft)
        actions.apply(1, draft)
        updated = self.db.one('SELECT * FROM consents WHERE user_id=1')['updated_at']
        self.now += 5
        actions.apply(1, draft)
        self.assertEqual(self.db.one('SELECT * FROM consents WHERE user_id=1')['updated_at'], updated)
        with self.assertRaises(AccessDenied):
            actions.prepare(1, 'consent', {'actor': 2, 'kind': 'ai', 'enabled': True})

    def test_stale_pair_draft_and_expiry(self):
        self.member(2)
        pair = self.pair(1, 2)
        actions = Actions(self.db)
        draft = actions.prepare(1, 'quiet_hours', {'start': '22:00', 'end': '08:00'})
        with self.db.transaction():
            self.db.conn.execute("UPDATE pairs SET status='ended' WHERE id=?", (pair,))
            self.db.conn.execute('UPDATE pair_members SET active=0 WHERE pair_id=?', (pair,))
        with self.assertRaises(Conflict):
            actions.apply(1, draft)
        draft = actions.prepare(1, 'quiet_hours', {'start': '22:00', 'end': '08:00'})
        self.now += 86401
        with self.assertRaises(Conflict):
            actions.apply(1, draft)
        self.assertEqual(self.db.user(1)['quiet_start'], '23:00')


class MigrationTests(unittest.TestCase):
    def test_legacy_migration_preserves_data_is_repeatable_and_resets_consent(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)/'legacy.sqlite'
            old = LegacyStore(path)
            old.user(1, 'Alice')
            old.user(2, 'Bob')
            old.request(1, old.user(2)['code'])
            old.accept(2, 1)
            pid = old.add(1, 'Cafe', 'Food', 'Private note')
            old.toggle_like(2, pid)
            with old.conn:
                old.conn.execute('UPDATE users SET ai=1')
            old.conn.close()
            db = Database(path)
            pair = db.pair(1)
            self.assertEqual(db.one('SELECT title,description FROM places WHERE id=?', (pid,)),
                             {'title': 'Cafe', 'description': 'Private note'})
            self.assertEqual(len(db.all('SELECT * FROM place_preferences')), 2)
            self.assertFalse(db.all('SELECT * FROM consents'))
            self.assertTrue(path.with_name(path.name+'.pre-v1.sqlite').exists())
            self.assertEqual(db.conn.execute('PRAGMA foreign_key_check').fetchall(), [])
            db.close()
            db = Database(path)
            self.assertEqual(db.pair(1), pair)
            self.assertEqual(len(db.all('SELECT * FROM places')), 1)
            db.close()


if __name__ == '__main__':
    unittest.main()
