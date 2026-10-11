import secrets
import sqlite3
from pathlib import Path
import time


class Store:
    def __init__(self, path):
        Path(path).parent.mkdir(parents=True, exist_ok=True)
        self.conn = sqlite3.connect(path)
        self.conn.row_factory = sqlite3.Row
        self.conn.executescript('''
        PRAGMA foreign_keys=ON;
        PRAGMA journal_mode=WAL;
        CREATE TABLE IF NOT EXISTS users(
            id INTEGER PRIMARY KEY, name TEXT NOT NULL,
            code TEXT UNIQUE NOT NULL, partner INTEGER REFERENCES users(id),
            ai INTEGER NOT NULL DEFAULT 0);
        CREATE TABLE IF NOT EXISTS requests(
            sender INTEGER PRIMARY KEY REFERENCES users(id),
            target INTEGER NOT NULL REFERENCES users(id), created REAL NOT NULL);
        CREATE TABLE IF NOT EXISTS places(
            id INTEGER PRIMARY KEY, owner INTEGER NOT NULL REFERENCES users(id),
            partner INTEGER NOT NULL REFERENCES users(id),
            title TEXT NOT NULL, category TEXT NOT NULL, note TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS likes(
            place INTEGER REFERENCES places(id) ON DELETE CASCADE,
            user INTEGER REFERENCES users(id), PRIMARY KEY(place,user));
        CREATE TABLE IF NOT EXISTS cooldowns(
            user INTEGER, action TEXT, at REAL, PRIMARY KEY(user,action));
        ''')

    def user(self, uid, name=None):
        if name is not None:
            with self.conn:
                while not self.conn.execute('SELECT 1 FROM users WHERE id=?', (uid,)).fetchone():
                    try:
                        self.conn.execute('INSERT INTO users(id,name,code) VALUES(?,?,?)',
                                          (uid, name, secrets.token_hex(5).upper()))
                    except sqlite3.IntegrityError:
                        continue
                self.conn.execute('UPDATE users SET name=? WHERE id=?', (name, uid))
        return self.conn.execute('SELECT * FROM users WHERE id=?', (uid,)).fetchone()

    def request(self, uid, code):
        with self.conn:
            me = self.user(uid)
            other = self.conn.execute('SELECT * FROM users WHERE code=?', (code.upper(),)).fetchone()
            if not other or other['id'] == uid or me['partner'] or other['partner']:
                raise ValueError('Код не найден, принадлежит вам или один из вас уже в паре.')
            self.conn.execute('INSERT OR REPLACE INTO requests VALUES(?,?,?)', (uid, other['id'], time.time()))
            return other['id']

    def accept(self, target, sender):
        with self.conn:
            request = self.conn.execute('SELECT * FROM requests WHERE sender=? AND target=?', (sender, target)).fetchone()
            if not request or time.time() - request['created'] > 86400:
                raise ValueError('Приглашение истекло или уже обработано.')
            if self.user(target)['partner'] or self.user(sender)['partner']:
                raise ValueError('Один из вас уже в паре.')
            self.conn.execute('UPDATE users SET partner=?,ai=0 WHERE id=?', (sender, target))
            self.conn.execute('UPDATE users SET partner=?,ai=0 WHERE id=?', (target, sender))
            self.conn.execute('DELETE FROM requests WHERE sender IN (?,?) OR target IN (?,?)', (sender,target,sender,target))

    def unpair(self, uid):
        with self.conn:
            partner = self.user(uid)['partner']
            if partner:
                self.conn.execute('DELETE FROM places WHERE (owner=? AND partner=?) OR (owner=? AND partner=?)', (uid,partner,partner,uid))
                self.conn.execute('UPDATE users SET partner=NULL,ai=0,code=CASE WHEN id=? THEN ? ELSE ? END WHERE id IN (?,?)',
                                  (uid,secrets.token_hex(5).upper(),secrets.token_hex(5).upper(),uid,partner))
            return partner

    def available(self, uid, action, seconds):
        with self.conn:
            row = self.conn.execute('SELECT at FROM cooldowns WHERE user=? AND action=?', (uid,action)).fetchone()
            if row and time.time() - row['at'] < seconds:
                return False
            self.conn.execute('INSERT OR REPLACE INTO cooldowns VALUES(?,?,?)', (uid,action,time.time()))
            return True

    def places(self, uid, category=None, mutual=False):
        partner = self.user(uid)['partner']
        if not partner:
            return []
        rows = self.conn.execute('''SELECT p.*,
            EXISTS(SELECT 1 FROM likes WHERE place=p.id AND user=?) AS mine,
            EXISTS(SELECT 1 FROM likes WHERE place=p.id AND user=?) AS theirs
            FROM places p WHERE (owner=? AND partner=?) OR (owner=? AND partner=?)
            ORDER BY p.id DESC''', (uid,partner,uid,partner,partner,uid)).fetchall()
        return [r for r in rows if (not category or r['category']==category) and (not mutual or r['mine'] and r['theirs'])]

    def add(self, uid, title, category, note):
        partner = self.user(uid)['partner']
        if not partner:
            raise ValueError('Сначала создайте пару.')
        with self.conn:
            cur = self.conn.execute('INSERT INTO places(owner,partner,title,category,note) VALUES(?,?,?,?,?)', (uid,partner,title,category,note))
            self.conn.execute('INSERT INTO likes VALUES(?,?)', (cur.lastrowid,uid))
            return cur.lastrowid

    def toggle_like(self, uid, pid):
        if not any(r['id']==pid for r in self.places(uid)):
            raise ValueError('Место недоступно.')
        with self.conn:
            cur = self.conn.execute('DELETE FROM likes WHERE place=? AND user=?', (pid,uid))
            if not cur.rowcount:
                self.conn.execute('INSERT INTO likes VALUES(?,?)', (pid,uid))

    def delete(self, uid, pid):
        if not any(r['id']==pid for r in self.places(uid)):
            raise ValueError('Место недоступно.')
        with self.conn:
            self.conn.execute('DELETE FROM places WHERE id=? AND owner=?', (pid,uid))
