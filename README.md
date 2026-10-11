# RelationshipChatBot 💕

> **Текущий план: первый релиз на Go.** Реализация в `cmd/` и `internal/`, команды и статус — [docs/go-development.md](docs/go-development.md). Подключение Go-кода к Telegram проверено. Полный релиз и постоянный запуск ещё не готовы. Разделы ниже описывают исторический Python-прототип и не являются инструкцией запуска новой версии; старый бот нельзя открывать на мигрированной базе.

Небольшой Telegram-бот для пары: знаки внимания и общие планы.

## Что работает

- Личный код дружбы, ввод кода партнёра и подтверждение приглашения (24 часа).
- «Хочу внимания» и «Думаю о тебе»: уведомления партнёру, ограничение раз в 30 секунд.
- Общие места: название, категория, адрес/ссылка/заметка. Категории: прогулка, еда, музыка, кино, поездка, другое.
- Сердечки каждого партнёра, список «Нравится обоим», случайный выбор из совпадений.
- Автор может удалить своё место. Завершение пары с подтверждением удаляет общую коллекцию и обновляет коды.
- Необязательный DeepSeek: план свидания и короткий совет по общению из мест, которые нравятся обоим.
- SQLite на диске, systemd с перезапуском, тесты и шаблон GitHub Actions.

Python 3.11+, aiogram 3, long polling. Домен, HTTPS-сертификат, открытый веб-порт, Redis и отдельный PostgreSQL не нужны. Запускайте **один** процесс бота на один токен.

## 1. Создать бота

В Telegram откройте официальный [@BotFather](https://t.me/BotFather), отправьте `/newbot`, задайте имя и username. Полученный токен храните только в `.env`. Не публикуйте его и не присылайте в чат. В `/setcommands` можно добавить:

```text
start - Главное меню
cancel - Отменить ввод
ai - Настройки ИИ
```

## 2. Проверить локально (Windows / PowerShell)

```powershell
python -m venv .venv
.\.venv\Scripts\python -m pip install -r requirements.txt
Copy-Item .env.example .env
notepad .env
.\.venv\Scripts\python -m app.bot
```

Заполните `BOT_TOKEN`. Остальное можно оставить как есть. Оба партнёра должны открыть бота и нажать `/start`: Telegram не позволяет боту первым начать личный диалог. Затем «Наша пара» → обмен кодами → принятие приглашения.

Добавьте место от одного аккаунта, поставьте сердечко от другого и проверьте «Нравится обоим». Проверьте обе кнопки уведомлений. Остановить локальную копию: Ctrl+C, прежде чем запускать VPS.

## 3. Минимальный VPS

Для одной пары рекомендован 1 vCPU, **1 ГБ RAM**, 10–20 ГБ диска, Ubuntu 24.04 LTS. 512 МБ можно попробовать без Docker, но 1 ГБ оставляет запас ОС и установке пакетов. ИИ работает через API, GPU и локальная модель не нужны.

Проверено по официальным страницам 11 октября 2026:

| Провайдер | Конфигурация | Цена на странице |
|---|---|---|
| [RUVDS Старт Хит](https://ruvds.com/ru/vps_start/) | 1 CPU, 1 ГБ, 20 ГБ HDD, 1 IP | 279 ₽/мес |
| [RUVDS Старт](https://ruvds.com/ru/vps_start/) | 1 CPU, 512 МБ, 10 ГБ HDD, 1 IP | 149 ₽/мес, есть ограничения |
| [Timeweb Cloud-15](https://timeweb.cloud/services/vds-vps) | 1 CPU, 1 ГБ, 15 ГБ NVMe | 1 380 ₽/мес на открытой странице |

Я бы взял RUVDS «Старт Хит» для этого MVP. Перед оплатой проверьте итоговую стоимость, доступность Ubuntu и исходящих HTTPS-запросов к Telegram и DeepSeek; условия дешёвых тарифов и цены могут меняться. Сервер здесь не заказан и не оплачен.

## 4. Установка на Ubuntu

Подключитесь по SSH. Следующие команды выполняются на VPS с административными правами:

```bash
sudo apt update
sudo apt install -y python3 python3-venv git
sudo useradd --system --home /opt/relationship-bot --shell /usr/sbin/nologin relationshipbot
sudo git clone https://github.com/NikitaKashko/RelationshipChatBot.git /opt/relationship-bot
cd /opt/relationship-bot
sudo python3 -m venv .venv
sudo .venv/bin/pip install -r requirements.txt
sudo cp .env.example .env
sudo nano .env
sudo mkdir -p data
sudo chown relationshipbot:relationshipbot data .env
sudo chmod 700 data
sudo chmod 600 .env
sudo cp deploy/relationship-bot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now relationship-bot
sudo systemctl status relationship-bot
```

Логи: `sudo journalctl -u relationship-bot -n 50 --no-pager`. Бот требует исходящий HTTPS, входящие порты кроме SSH ему не нужны.

Обновление:

```bash
cd /opt/relationship-bot
sudo systemctl stop relationship-bot
sudo git pull --ff-only
sudo .venv/bin/pip install -r requirements.txt
sudo systemctl start relationship-bot
```

Сделайте резервное копирование SQLite через Python backup API (он безопасен при работающем боте):

```bash
sudo install -d -m 700 /var/backups/relationship-bot
sudo python3 -c "import sqlite3; from datetime import datetime; source=sqlite3.connect('/opt/relationship-bot/data/bot.db'); target=sqlite3.connect('/var/backups/relationship-bot/'+datetime.now().strftime('%Y%m%d-%H%M%S')+'.db'); source.backup(target); target.close(); source.close()"
```

Скачивайте копии на свой компьютер; бэкап на том же VPS не спасает от потери сервера. Для восстановления остановите сервис, замените базу резервной копией, удалите устаревшие `bot.db-wal` и `bot.db-shm` при остановленном сервисе, верните владельца `relationshipbot` и запустите сервис.

## 5. Подключить DeepSeek (по желанию)

Создайте API-ключ в [DeepSeek Platform](https://platform.deepseek.com/), пополните баланс и добавьте `DEEPSEEK_API_KEY` в `.env`, затем перезапустите сервис. Модель задаётся `DEEPSEEK_MODEL` (по умолчанию `deepseek-chat`; проверьте актуальные доступные модели в [документации](https://api-docs.deepseek.com/quick_start/pricing/)). Используется [официальный chat completions API](https://api-docs.deepseek.com/guides/multi_round_chat/).

Оба партнёра отдельно включают согласие через `/ai`. DeepSeek получает только названия и категории максимум 30 совпавших мест. Заметки, имена и Telegram ID не отправляются. Название места само может содержать личные сведения — учитывайте это при включении ИИ. Запросы ограничены раз в минуту на человека, ответ — 600 токенов. Это не месячный лимит расходов: контролируйте баланс в панели провайдера.

ИИ предлагает идеи из вашей коллекции, не ищет новые заведения в интернете и не проверяет адреса, цены и часы работы. Отдельного чата для произвольных советов в MVP нет.

## Разработка и ограничения MVP

```bash
python -m unittest discover -s tests -v
python -m compileall -q app
```

Проверяются подтверждение пары, изоляция коллекций, взаимные лайки, истечение приглашений, ограничение частоты и завершение пары. Живой Telegram и DeepSeek нужно проверить после добавления ключей.

Для автоматического CI скопируйте `deploy/github-actions-tests.yml` в `.github/workflows/tests.yml` и закоммитьте через аккаунт с правом `workflow`. Шаблон подготовлен, но автоматические проверки в GitHub пока не включены.

Состояние незавершённого добавления места хранится в памяти: после перезапуска начните ввод заново. Завершённые места и пары сохраняются. Доставка уведомлений не имеет очереди повторных попыток. SQLite и синхронные короткие запросы подходят для малого личного проекта; при большом числе пользователей стоит перейти на асинхронное хранилище.

Публичный репозиторий показывает **исходники**, а не базу вашей пары: `.env`, `data/` и базы исключены из Git. Не добавляйте других участников с правами записи, если хотите менять основной репозиторий самостоятельно. Другие люди могут просматривать и делать fork/предлагать PR; запись в ваш репозиторий этим не предоставляется. Лицензия намеренно не добавлена: при желании выберите её отдельно.
