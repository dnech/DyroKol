# Развёртывание

## Сеть и фаервол

| Порт | Где | Нужно ли открывать |
| --- | --- | --- |
| `4443/tcp` (или ваш `--listen`) | relay | **Да**: сюда подключаются агенты |
| порты входных точек на relay (например, `8080`) | relay | Да, если посетители приходят снаружи |
| админка | relay | **Порта нет вообще:** она отдаётся только через туннель админ-машины (необязательный вход `--admin`, если включите, должен оставаться на loopback) |
| порты входных точек на агентах | агент | Только если нужны извне; по умолчанию слушают на всех интерфейсах |

Агентам входящие соединения не нужны: они сами подключаются к relay. Порт туннеля можно
вынести на `443`: `--listen :443` (потребуется право на порты ниже 1024, см. ниже).

> Порты входных точек агента слушают на **всех** интерфейсах (`:порт`). Если порт нужен только
> локальному ПК, ограничьте источник фильтром `allow: 127.0.0.1`.

## Linux

### Установка одной командой

Скрипт [`scripts/install.sh`](../../scripts/install.sh) скачивает бинарник нужной архитектуры
(`amd64`, `arm64`, `arm`) со страницы [Releases](https://github.com/dnech/DyroKol/releases), проверяет
его SHA-256 по `SHA256SUMS` и кладёт в `/usr/local/bin/dyrokol`. С ключом `--role` он ещё создаёт
пользователя `dyrokol`, каталог `/var/lib/dyrokol` и systemd-юнит.

```bash
# только бинарник
curl -fsSL https://raw.githubusercontent.com/dnech/DyroKol/main/scripts/install.sh | sudo bash

# relay как служба systemd (сразу запускается)
curl -fsSL https://raw.githubusercontent.com/dnech/DyroKol/main/scripts/install.sh | sudo bash -s -- --role server

# агент как служба systemd (включается; запускать после регистрации, см. ниже)
curl -fsSL https://raw.githubusercontent.com/dnech/DyroKol/main/scripts/install.sh | sudo bash -s -- --role client
```

| Ключ | Описание |
| --- | --- |
| `--role server\|client` | дополнительно установить и включить юнит `dyrokol-server` / `dyrokol-client` |
| `--version vX.Y.Z` | конкретная версия (по умолчанию последняя) |
| `--prefix DIR` | куда положить бинарник (по умолчанию `/usr/local/bin`) |
| `--data-dir DIR` | каталог данных службы (по умолчанию `/var/lib/dyrokol`) |
| `--uninstall` | удалить бинарник и юниты (данные остаются); с `--purge` удаляется и каталог данных |

Скрипт можно скачать, прочитать и запустить локально: `sudo bash install.sh --role server`.
Он ставит только то, что описано выше, и проверяет контрольную сумму; при несовпадении установка
прерывается. Для зеркала или проверки без интернета задайте `DYROKOL_BASE_URL` (каталог с файлами
`dyrokol-linux-<arch>` и `SHA256SUMS`). В Termux скрипт ставит бинарник в `$PREFIX/bin` без
systemd (см. [Android](#android-termux)).

Скрипт создаёт `/var/lib/dyrokol/dyrokol.yaml` с **заглушками** и включает службу агента, не запуская её.
Два случая:

- **Машина уже работающего relay** (обычный случай): создайте машину в админке (**Machines → Add machine**),
  откройте `/var/lib/dyrokol/dyrokol.yaml`, впишите адрес relay (домен или IP) и токен машины, затем
  `sudo systemctl start dyrokol-client`.
- **Самая первая машина нового relay** (она инициализирует базу, один раз, от пользователя службы):

```bash
sudo -u dyrokol dyrokol client --dir /var/lib/dyrokol --server vps.example.com
# …прервите Ctrl+C, когда увидите «connected», затем:
sudo systemctl start dyrokol-client
```

### Установка вручную

```bash
sudo install -m 0755 dyrokol /usr/local/bin/dyrokol
sudo useradd --system --home /var/lib/dyrokol --create-home --shell /usr/sbin/nologin dyrokol
```

### Как сделать из DyroKol демон (службу)

DyroKol — обычный процесс переднего плана: он пишет лог в stderr, корректно завершается по
`SIGTERM`/`SIGINT` и сам переподключается при обрыве. Поэтому любой менеджер процессов подойдёт;
собственный `fork`/`daemonize` не нужен (и не поддерживается). Ниже рабочие варианты, от
рекомендуемого к запасным. Во всех примерах бинарник лежит в `/usr/local/bin/dyrokol`, данные — в
`/var/lib/dyrokol`; для агента замените `server` на `client`.

#### systemd: relay

`/etc/systemd/system/dyrokol-server.service`:

```ini
[Unit]
Description=DyroKol relay
After=network-online.target
Wants=network-online.target

[Service]
User=dyrokol
ExecStart=/usr/local/bin/dyrokol server --dir /var/lib/dyrokol
# постоянный токен админки (необязательно): файл с токеном, права 0600, владелец dyrokol
Environment=DYROKOL_ADMIN_TOKEN_FILE=/etc/dyrokol/admin-token
# нужно только для портов ниже 1024 (443, 80, …)
AmbientCapabilities=CAP_NET_BIND_SERVICE
Restart=on-failure
RestartSec=3
# базовое укрепление
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/dyrokol
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now dyrokol-server
journalctl -u dyrokol-server -f
```

В логе будет сказано, что админка доступна только через админ-машину; токен админки (создаётся один раз и хранится в зашифрованной базе) печатает админ-машина как `admin ui token`. Изменения портов в админке
применяются без перезапуска службы.

#### systemd: агент

`/etc/systemd/system/dyrokol-client.service`:

```ini
[Unit]
Description=DyroKol agent
After=network-online.target
Wants=network-online.target

[Service]
User=dyrokol
ExecStart=/usr/local/bin/dyrokol client --dir /var/lib/dyrokol
AmbientCapabilities=CAP_NET_BIND_SERVICE
Restart=always
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/dyrokol

[Install]
WantedBy=multi-user.target
```

Перед первым запуском службы зарегистрируйте машину (см. выше) или подготовьте конфиг
`/var/lib/dyrokol/dyrokol.yaml` (см. [CLI](cli.md#файл-конфигурации-агента)) с правами `0600` и
владельцем `dyrokol`. Агент переподключается сам: потеря сети или перезапуск relay не требуют
вмешательства.

#### systemd без root (пользовательская служба)

Подходит для домашнего ПК или Raspberry Pi, где нет доступа к `/etc`. Файл
`~/.config/systemd/user/dyrokol-client.service`:

```ini
[Unit]
Description=DyroKol agent
After=network-online.target

[Service]
ExecStart=%h/bin/dyrokol client
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now dyrokol-client
loginctl enable-linger "$USER"      # запускать и без входа в систему, с загрузки
journalctl --user -u dyrokol-client -f
```

Порты ниже 1024 без root здесь недоступны.

#### OpenRC (Alpine, Gentoo)

`/etc/init.d/dyrokol-client`:

```sh
#!/sbin/openrc-run
description="DyroKol agent"
command="/usr/local/bin/dyrokol"
command_args="client --dir /var/lib/dyrokol"
command_user="dyrokol:dyrokol"
command_background=true
pidfile="/run/${RC_SVCNAME}.pid"
output_log="/var/log/dyrokol-client.log"
error_log="/var/log/dyrokol-client.log"
supervisor=supervise-daemon     # перезапуск при падении

depend() {
    need net
}
```

```bash
sudo chmod +x /etc/init.d/dyrokol-client
sudo rc-update add dyrokol-client default
sudo rc-service dyrokol-client start
```

#### SysV init (старые системы без systemd)

`/etc/init.d/dyrokol-client`:

```sh
#!/bin/sh
### BEGIN INIT INFO
# Provides:          dyrokol-client
# Required-Start:    $network $remote_fs
# Required-Stop:     $network $remote_fs
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Description:       DyroKol agent
### END INIT INFO
BIN=/usr/local/bin/dyrokol
ARGS="client --dir /var/lib/dyrokol"
PIDFILE=/var/run/dyrokol-client.pid
LOG=/var/log/dyrokol-client.log

case "$1" in
  start)
    start-stop-daemon --start --background --make-pidfile --pidfile "$PIDFILE" \
      --chuid dyrokol --startas /bin/sh -- -c "exec $BIN $ARGS >>$LOG 2>&1" ;;
  stop)
    start-stop-daemon --stop --pidfile "$PIDFILE" --retry TERM/20/KILL/5 && rm -f "$PIDFILE" ;;
  restart) "$0" stop; "$0" start ;;
  status)
    start-stop-daemon --status --pidfile "$PIDFILE" && echo running || echo stopped ;;
  *) echo "usage: $0 {start|stop|restart|status}"; exit 1 ;;
esac
```

```bash
sudo chmod +x /etc/init.d/dyrokol-client
sudo update-rc.d dyrokol-client defaults      # Debian/Ubuntu
sudo service dyrokol-client start
```

Такой скрипт не перезапускает процесс при падении; если это важно, используйте `supervisor` (ниже).

#### supervisor

`/etc/supervisor/conf.d/dyrokol-client.conf`:

```ini
[program:dyrokol-client]
command=/usr/local/bin/dyrokol client --dir /var/lib/dyrokol
user=dyrokol
autostart=true
autorestart=true
stopsignal=TERM
stdout_logfile=/var/log/dyrokol-client.log
redirect_stderr=true
```

```bash
sudo supervisorctl reread && sudo supervisorctl update
```

#### Запасной вариант: `@reboot` и tmux/nohup

Если нет ни одного из менеджеров выше (например, на общем хостинге с cron), агент можно запускать
из `crontab -e`:

```cron
@reboot nohup /home/me/bin/dyrokol client >>/home/me/.dyrokol.log 2>&1 &
```

Это без перезапуска при падении и без ротации логов, поэтому годится как временное решение.

## Windows

### Запуск вручную

1. Положите `dyrokol.exe` в каталог, например `C:\dyrokol`.
2. Запустите в PowerShell: `.\dyrokol.exe client --server vps.example.com:4443`
   (или `.\dyrokol.exe server` для relay).

Файлы (конфиг, `pins.json`, база relay) окажутся в `%USERPROFILE%\.config\dyrokol` либо в папке,
заданной `--dir`. Первый запуск relay может вызвать запрос брандмауэра Windows: разрешите входящие
подключения на нужные порты.

### Служба Windows

`dyrokol.exe` умеет сам регистрироваться как служба Windows. Команды выполняются в PowerShell,
**запущенном от имени администратора**:

```powershell
# Установить и сразу запустить
.\dyrokol.exe service install client --server vps.example.com:4443
.\dyrokol.exe service install server                 # или relay

# Управление
.\dyrokol.exe service status
.\dyrokol.exe service stop
.\dyrokol.exe service start
.\dyrokol.exe service restart

# Снять с регистрации (данные остаются)
.\dyrokol.exe service uninstall
```

После `install` указывается роль (`server` или `client`) и любые её флаги: они записываются в
командную строку службы как есть.

| Ключ `service install` | Описание |
| --- | --- |
| `--name ИМЯ` | имя службы (по умолчанию `DyroKol`); позволяет поставить relay и агента рядом, например `--name DyroKolServer` |
| `--manual` | запуск вручную вместо автоматического при загрузке |
| `--no-start` | не запускать службу сразу после установки |

Что делает установка:

- запоминает **полный путь** к `dyrokol.exe`: после установки не переносите файл (или переустановите службу);
- если среди флагов роли нет `--dir`/`--config`, добавляет `--dir %ProgramData%\DyroKol`: служба работает
  от `LocalSystem`, у которой нет обычной домашней папки;
- создаёт этот каталог и оставляет доступ только `SYSTEM` и администраторам (в нём лежат токены и ключи);
- ставит автозапуск с задержкой после загрузки и **перезапуск при сбое** (через 5, 5 и 30 секунд);
- пишет лог службы в `<каталог данных>\dyrokol-service.log`.

`service install client` создаёт `<каталог данных>\dyrokol.yaml` с **заглушками** (адрес relay, токен) и не
запускает службу, пока их не заполнят:

```powershell
.\dyrokol.exe service install client
# отредактируйте C:\ProgramData\DyroKol\dyrokol.yaml: адрес relay и токен этой машины
# (админка: Machines → Add machine), затем:
.\dyrokol.exe service start
```

Самую первую машину нового relay регистрируют иначе (она инициализирует базу, работает один раз и должна
использовать тот же каталог данных):

```powershell
.\dyrokol.exe client --dir C:\ProgramData\DyroKol --server vps.example.com
# …прервите Ctrl+C, когда увидите «connected»
.\dyrokol.exe service install client --dir C:\ProgramData\DyroKol
```

При остановке диспетчер служб просит процесс завершиться, и он закрывается так же аккуратно, как по
Ctrl+C: relay дописывает статистику и запечатывает базу.

> Службой можно управлять и штатными средствами: `services.msc`, `sc.exe query DyroKol`,
> `Get-Service DyroKol`.

### Планировщик заданий (запасной вариант)

Если служба не подходит, создайте задание в **Планировщике заданий**: триггер «При входе в систему»
или «При запуске», действие — запуск `C:\dyrokol\dyrokol.exe` с аргументом `client` и «Рабочая
папка» `C:\dyrokol`; включите «Выполнять независимо от регистрации пользователя» и «Перезапускать
при сбое».

## Android (Termux)

Используйте сборку `dyrokol-termux-arm64` (или `dyrokol-termux-arm` для 32-битных устройств) со страницы [Releases](https://github.com/dnech/DyroKol/releases), либо установочный скрипт: `curl -fsSL https://raw.githubusercontent.com/dnech/DyroKol/main/scripts/install.sh | bash`.

```bash
cp dyrokol-termux-arm64 ~/dyrokol && chmod +x ~/dyrokol
~/dyrokol client --server vps.example.com:4443
```

Чтобы агент не засыпал при выключенном экране, включите `termux-wake-lock` и отключите для Termux
оптимизацию батареи. Для автозапуска при загрузке устройства используйте приложение **Termux:Boot**
(скрипт в `~/.termux/boot/`, который запускает `dyrokol client`) или `tmux`/`termux-services`.

> Сборки для Android проверены только на соответствие формату исполняемых файлов (ELF ARM/AArch64);
> на реальном устройстве их стоит проверить самостоятельно.

## Обновление

1. Сделайте бэкап (**Settings → Download backup**) и скопируйте каталог данных relay.
2. Остановите службу, замените бинарник, запустите. На Linux это делает повторный запуск
   `install.sh` (он заменяет бинарник; службу затем перезапустите: `systemctl restart dyrokol-server`).
   На Windows: `dyrokol service stop`, замена `dyrokol.exe`, `dyrokol service start`.
3. База и протокол версионируются: relay отказывается подключаться к агентам с другой версией
   протокола (`protocol version … not supported`), а базу другой версии схемы отвергает с понятной
   ошибкой. Обновляйте relay и агентов одновременно, пока версия протокола меняется.

Версия протокола и приложения видна на странице **Settings → About this relay**.

## Резервное копирование

Есть два уровня:

- **Бэкап конфигурации** (`dyrokol-config.json`): машины (по хэшам), порты, точки, маршруты, раскладка схемы,
  настройки. Подходит для переноса на другой relay. После восстановления агентам нужны **их же
  токены** (бэкап хранит только хэши), а пароль базы нового relay задаёт его инициализация.
- **Копия каталога данных relay** (`*.db.enc`, сертификат и ключ): сохраняет всё, включая
  статистику и отпечаток TLS. Копируйте при остановленном relay или сразу после записи: файлы
  заменяются атомарно, поэтому копия остаётся целой в любой момент.

Потеря `dyrokol.crt`/`dyrokol.key` означает новый отпечаток: каждому агенту придётся удалить
запись из `pins.json`. Потеря пароля базы означает потерю доступа к `*.db.enc`: пароль восстановить
невозможно, останется только бэкап `dyrokol-config.json`.

## Перенос relay на другой сервер

Токены машин при переносе остаются прежними, потому что бэкап хранит их хэши. Меняются только
адрес relay и пароль базы.

1. На старом relay скачайте бэкап (**Settings → Download backup**).
2. Перенесите `dyrokol.crt` и `dyrokol.key` в каталог данных нового relay, чтобы отпечаток
   не изменился. Иначе каждому агенту придётся удалить запись из `pins.json`.
3. Запустите relay на новом сервере и зарегистрируйте временную админ-машину:
   `dyrokol client --server новый-адрес:4443`. Это создаёт **новую базу с новым паролем**;
   пароль лежит в конфиге этой машины (`db_password`).
4. Откройте админку этой машины и восстановите бэкап (**Settings → Restore**). Временная машина
   исчезнет вместе с остальной старой конфигурацией, а все агенты будут отключены. Остановите
   временного агента: он будет безуспешно переподключаться (в журнале relay — `unknown token`).
5. На каждом агенте (включая прежнюю админ-машину) измените в конфиге `addr` на новый адрес
   и, если в конфиге есть `db_password`, замените его **новым паролем из шага 3**. Старый пароль
   не подходит к новой базе, и relay не разблокируется после перезапуска, пока кто-нибудь не
   предъявит правильный.
6. Запустите агентов: они подключатся со своими прежними токенами.

## Запуск за обратным прокси

Туннель использует собственный TLS поверх TCP, поэтому HTTP-прокси перед ним не нужен. Если
порт всё же проходит через балансировщик, он должен работать как чистый **TCP-passthrough** без
терминации TLS. PROXY protocol не поддерживается, а relay будет видеть адрес балансировщика
вместо адреса агента.

Админку наружу выставлять не рекомендуется. Если вы всё же ставите перед ней прокси: проверка
`Origin` сравнивает его с заголовком `Host`, поэтому прокси обязан передавать оригинальный `Host`.
Защиту сетью и TLS в этом случае вы обеспечиваете сами.
