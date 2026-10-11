import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

from app import bot
from app.db import Store


class BotTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = Store(str(Path(self.tmp.name)/'test.db'))
        self.store.user(1,'Alice')
        self.store.user(2,'Bob')
        self.store.request(1,self.store.user(2)['code'])
        self.store.accept(2,1)
        self.patch = patch.object(bot,'db',self.store)
        self.patch.start()
        self.message = SimpleNamespace(from_user=SimpleNamespace(id=1,first_name='Alice'),
                                       text='💌 Хочу внимания',bot=SimpleNamespace(send_message=AsyncMock()),answer=AsyncMock())

    async def asyncTearDown(self):
        self.patch.stop()
        self.store.conn.close()
        self.tmp.cleanup()

    async def test_attention_goes_to_partner_and_is_rate_limited(self):
        await bot.affection(self.message)
        self.message.bot.send_message.assert_awaited_once_with(2,'Alice хочет вашего внимания 💌',reply_markup=None)
        await bot.affection(self.message)
        self.assertEqual(self.message.bot.send_message.await_count,1)

    async def test_ai_requires_both_consents_before_network(self):
        with patch.dict('os.environ',{'DEEPSEEK_API_KEY':'test-only'}), patch.object(bot.httpx,'AsyncClient') as client:
            await bot.ai(self.message)
            client.assert_not_called()
        self.assertIn('согласие обоих',self.message.answer.call_args.args[0])

    async def test_ai_sends_only_mutual_titles_and_categories(self):
        pid = self.store.add(1,'Park','Walk','Private address')
        self.store.toggle_like(2,pid)
        self.store.add(1,'Unmatched cafe','Food','Another secret')
        with self.store.conn:
            self.store.conn.execute('UPDATE users SET ai=1')
        response = SimpleNamespace(raise_for_status=lambda:None,json=lambda:{'choices':[{'message':{'content':'Go to Park'}}]})
        client = AsyncMock()
        client.post.return_value = response
        manager = AsyncMock()
        manager.__aenter__.return_value = client
        with patch.dict('os.environ',{'DEEPSEEK_API_KEY':'test-only'}), patch.object(bot.httpx,'AsyncClient',return_value=manager):
            await bot.ai(self.message)
        payload = client.post.call_args.kwargs['json']['messages'][1]['content']
        self.assertIn('Park',payload)
        for private in ('Private address','Alice','Bob','Unmatched cafe'):
            self.assertNotIn(private,payload)
        self.assertEqual(self.message.answer.call_args.args[0],'Go to Park')

    def test_menu_buttons_are_not_consumed_as_place_text(self):
        for text in bot.MENU_TEXTS:
            self.assertFalse(bot.INPUT_TEXT.resolve(SimpleNamespace(text=text)))
        self.assertTrue(bot.INPUT_TEXT.resolve(SimpleNamespace(text='My cafe')))
