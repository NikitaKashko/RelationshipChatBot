"""Check credentials without logging tokens, HTTP URLs or exception messages."""

import asyncio
from pathlib import Path

from aiogram import Bot
from dotenv import dotenv_values


async def check():
    settings = dotenv_values(Path(__file__).resolve().parents[1] / '.env')
    token = settings.get('BOT_TOKEN')
    if not token:
        print('BOT_TOKEN_MISSING')
        return 1
    try:
        async with Bot(token) as bot:
            identity = await bot.get_me()
            webhook = await bot.get_webhook_info()
            print(f'BOT_OK @{identity.username}')
            print(f'WEBHOOK_CONFIGURED={bool(webhook.url)}')
            return 0
    except Exception as exc:
        print(f'BOT_CHECK_FAILED {type(exc).__name__}')
        return 1


if __name__ == '__main__':
    raise SystemExit(asyncio.run(check()))
