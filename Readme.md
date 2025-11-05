# Social Network

Монолит и сервис диалогов в отдельном микросервисе. В рамках домашнего задания по курсу "Микросервисная архитектура и Highload" для X5
добавлена репликация PostgreSQL (master + 2 slaves, без Patroni, с ручным failover через pg_promote),
read/write separation в монолите, тестовая таблица logs для нагрузки на запись.

## Структура проекта

├── monolith/                # Основное приложение (эндпоинты /user/get и /user/search)
│   ├── main.go              # Основной код приложения (master/slave DB)
│   ├── go.mod               # Зависимости Go
│   ├── go.sum               # Зависимости Go
│   ├── Dockerfile           # Сборка образа монолита
│   └── people.v2.csv.zip    # Данные для генерации пользователей (разархивировать при необходимости)'
├── dialog-service/          # Директория микросервиса диалогов
│   ├── main.go              # Код Dialog Service
│   ├── go.mod               # Зависимости Go
│   ├── go.sum               # Зависимости Go
│   └── Dockerfile           # Сборка образа микросервиса'
├── setup-replica.sh         # Автоматическая настройка реплик (pg_basebackup + primary_conninfo)
├── go.mod                   # Зависимости Go
├── go.sum                   # Зависимости Go
├── prometheus.yml           # Конфигурация Prometheus
├── grafana-datasources.yml  # Источники данных Grafana
├── grafana-dashboards.yml   # Дашборды Grafana
├── dashboard.json           # Готовый JSON-дашборд Grafana
├── dashboard_compat.yml     # Совместимость метрик с node-exporter
├── wrk_mix.lua              # Скрипт нагрузки (50/50 get/search)
├── REPORT.md                # Отчёт по проделанной работе
├── Readme.md                # Основная документация проекта
├── wrk_read_mix.lua         # Lua - скрипт нагрузочного тестирования чтения (эксперимент 1)
├── insert_logs.go           # Go - скрипт нагрузочного тестирования записи (экмперимент 2)
├── failover.sh              # Скрипт для остановки master, промоута slave2 до мастера и подключения к нему slave1
├── reset_db_cluster.sh      # Скрипт для восстановления PG кластера к изначальному состоянию
├── initdb/
│   └── 10-initdb-listen.sh  # Скрипт подготовки конфигурации postgresql.conf и pg_hba.conf
└── postman/                 # Коллекции для тестирования
    └── microservices.json   # Тесты
```

## Запуск


```bash
# 1. Клонируем репозиторий
git clone https://github.com/DatDomrachev/social-network-replication
cd social-network-replication

# 2. Запуск всех сервисов через docker-compose (master + 2 slaves)
docker compose up -d --build

# 3. Проверка работы сервисов
curl http://localhost:8080/health  # Монолит
curl http://localhost:8081/health  # Dialog Service

# Проверь репликацию (на master psql)
docker exec -it postgres-master psql -U user -d social_network -c "SELECT * FROM pg_stat_replication;"

# 4. Генерация данных (1M пользователей на master)
docker compose run monolith go run main.go -generate

# 5. Просмотр логов
docker compose logs -f

# 6. Остановка
docker compose down -v  # С очисткой volumes
```

## Архитектура

```
Client → Monolith (8080) ──────────→ Master PG (writes) / Slave PG (reads)
         │                          (Users, Posts, Logs, Friends)
         │
         └── /dialog/* requests
             │
             ↓
         Dialog Service (8081) ─────→ In-Memory Storage
                                      (Messages only)
```
PG кластер: Master + Slave1 + Slave2

## Cтек

- **Язык**: Go 1.23
- **Web Framework**: Gin
- **Хранилище**: PostgreSQL c репликацией (master/slaves), In-memory диалоги
- **Контейнеризация**: Docker & Docker Compose
- **Аутентификация**: JWT токены (упрощенная)
- **Кластер**: PostgreSQL master + 2 slaves
- **Tools**: wrk (чтение), Go insert_logs.go (запись)
- **Prometheus**: Сбор метрик с node-exporter, postgres-exporter и cadvisor
- **Grafana**: Визуализация метрик и мониторинг кластера
- **cAdvisor**: Метрики контейнеров Docker
- **node-exporter**: Системные метрики (CPU, RAM, FS)
- **postgres-exporter**: Метрики PostgreSQL

## API Endpoints

### Монолит (порт 8080)
- `GET /health` - Проверка работоспособности
- `POST /login` - Аутентификация
- `POST /user/register` - Регистрация
- `GET /user/get/{id}` - Получение профиля
- `GET /user/search` - Поиск пользователей
- `PUT /friend/set/{user_id}` - Добавить друга
- `POST /post/create` - Создать пост
- `GET /post/feed` - Лента новостей
- `POST /dialog/{user_id}/send` - Отправка сообщения *(проксируется)*
- `GET /dialog/{user_id}/list` - История диалога *(проксируется)*
- `POST /log/insert` - Тестовая запись в logs *(write to master, for load test)*

### Dialog Service (порт 8081)
- `GET /health` - Проверка работоспособности
- `POST /dialog/{user_id}/send` - Отправка сообщения
- `GET /dialog/{user_id}/list` - История диалога
- `GET /dialogs` - Все диалоги пользователя

##  Тестирование

### 1. Импорт Postman коллекций
- **Микросервисы**: `postman/microservices.json`

### 2. Ключевые тест-кейсы

#### Регистрация и аутентификация:
```bash
# Регистрация пользователя
curl -X POST http://localhost:8080/user/register \\
  -H "Content-Type: application/json" \\
  -d '{"first_name":"Ivan","second_name":"Ivanov","password":"ivan123"}'

# Логин
curl -X POST http://localhost:8080/login \\
  -H "Content-Type: application/json" \\
  -d '{"id":"<user-id>","password":"ivan123"}'

  # Get user (read from slave)
curl http://localhost:8080/user/get/<user-id>

# Search (read from slave)
curl "http://localhost:8080/user/search?first_name=И&second_name=Абр"

# Insert log
curl -X POST http://localhost:8080/log/insert \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"data":"Test data"}'
```

#### Диалоги (главная функция):
```bash
# Отправка сообщения через монолит
curl -X POST http://localhost:8080/dialog/<friend-id>/send \\
  -H "Authorization: Bearer <token>" \\
  -H "Content-Type: application/json" \\
  -d '{"text":"Привет! Это сообщение обрабатывается микросервисом!"}'

# Получение истории диалога
curl -X GET http://localhost:8080/dialog/<friend-id>/list \\
  -H "Authorization: Bearer <token>"
```

##  Проверка репликации

### 1. Обратная совместимость
Reads on slave, writes on master

### 2. Изоляция сервисов
```bash
# Прямой вызов Dialog Service (должен провалиться без заголовка)
curl -X POST http://localhost:8081/dialog/<user-id>/send \\
  -H "Content-Type: application/json" \\
  -d '{"text":"Test"}'
# 401 Unauthorized

# Прямой вызов с заголовком X-User-ID (успех)
curl -X POST http://localhost:8081/dialog/<user-id>/send \\
  -H "X-User-ID: <current-user-id>" \\
  -H "Content-Type: application/json" \\
  -d '{"text":"Direct call to microservice"}'
# 200 OK
```

### 3. Мониторинг состояния
```bash
# Health check показывает статус обоих сервисов
curl http://localhost:8080/health
# Ответ: {"status":"ok","service":"monolith","dialog_service_status":"ok"}

curl http://localhost:8081/health
# Ответ: {"status":"ok","service":"dialog-service","stats":{...}}
```

## Docker образы

### Сборка и публикация
```bash
# Сборка образов
docker build -t DatDomrachev/social-network-monolith ./monolith
docker build -t DatDomrachev/dialog-service ./dialog-service

# Публикация в Docker Hub
docker push DatDomrachev/social-network-monolith
docker push DatDomrachev/dialog-service
```

### Использование готовых образов
```yaml

# docker-compose.yml  
services:
  monolith:
    image: DatDomrachev/social-network-monolith:v2.0
    # ...
  
  dialog-service:
    image: DatDomrachev/dialog-service:v1.0
    # ...
```
