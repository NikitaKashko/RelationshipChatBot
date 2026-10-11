import tempfile
import unittest
from pathlib import Path
from app.db import Store


class StoreTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.db = Store(str(Path(self.tmp.name)/'test.db'))
        for uid in range(1,5):
            self.db.user(uid,f'User {uid}')

    def tearDown(self):
        self.db.conn.close()
        self.tmp.cleanup()

    def pair(self,a=1,b=2):
        self.db.request(a,self.db.user(b)['code'])
        self.db.accept(b,a)

    def test_pair_requires_target_confirmation(self):
        self.db.request(1,self.db.user(2)['code'])
        with self.assertRaises(ValueError):
            self.db.accept(3,1)
        self.assertIsNone(self.db.user(1)['partner'])
        self.db.accept(2,1)
        self.assertEqual(self.db.user(1)['partner'],2)
        with self.assertRaises(ValueError):
            self.db.request(3,self.db.user(2)['code'])

    def test_mutual_and_pair_isolation(self):
        self.pair()
        self.pair(3,4)
        pid = self.db.add(1,'Cafe','Food','Note')
        self.assertFalse(self.db.places(1,mutual=True))
        with self.assertRaises(ValueError):
            self.db.toggle_like(3,pid)
        self.db.toggle_like(2,pid)
        self.assertEqual(len(self.db.places(1,mutual=True)),1)
        self.db.delete(2,pid)
        self.assertEqual(len(self.db.places(1)),1)
        self.db.toggle_like(2,pid)
        self.assertFalse(self.db.places(1,mutual=True))

    def test_unpair_removes_old_collection_and_rotates_codes(self):
        self.pair()
        old_code = self.db.user(1)['code']
        pid = self.db.add(1,'Park','Walk','')
        self.db.unpair(1)
        self.assertIsNone(self.db.user(2)['partner'])
        self.assertNotEqual(old_code,self.db.user(1)['code'])
        self.pair(1,3)
        self.assertFalse(self.db.places(1))
        with self.assertRaises(ValueError):
            self.db.toggle_like(1,pid)

    def test_expiry_and_cooldown(self):
        self.db.request(1,self.db.user(2)['code'])
        with self.db.conn:
            self.db.conn.execute('UPDATE requests SET created=0')
        with self.assertRaises(ValueError):
            self.db.accept(2,1)
        self.assertTrue(self.db.available(1,'affection',30))
        self.assertFalse(self.db.available(1,'affection',30))


if __name__ == '__main__':
    unittest.main()
