------------------- вспомогательные функции -------------------
-- UTF-8 кириллица (А–я)
local cyr = {}
do
  for code = 0x410, 0x44F do
    local c = string.char(
      0xE0 + math.floor(code / 0x1000),
      0x80 + (math.floor(code / 0x40) % 0x40),
      0x80 + (code % 0x40)
    )
    table.insert(cyr, c)
  end
end

local function rand_name()
  local n, t = math.random(2, 4), {}
  for i = 1, n do t[#t + 1] = cyr[math.random(#cyr)] end
  return table.concat(t)
end

local function urlencode(s)
  return (s:gsub("([^%w%-_%.~])", function(ch)
    return string.format("%%%02X", string.byte(ch))
  end))
end

------------------- загрузка user_ids.txt -------------------
local user_ids = {}
do
  local f = io.open("user_ids.txt", "r")
  if not f then
    error("Не найден файл user_ids.txt — без него скрипт работать не будет")
  end
  for line in f:lines() do
    local s = line:match("%S+")
    if s then table.insert(user_ids, s) end
  end
  f:close()
  io.stderr:write("Loaded " .. tostring(#user_ids) .. " user IDs\n")
end

------------------- счётчики статусов -------------------
local threads = {}

function setup(thread)
  table.insert(threads, thread)
  thread:set("c2", 0) -- 2xx
  thread:set("c4", 0) -- 4xx
  thread:set("c5", 0) -- 5xx
  thread:set("co", 0) -- other
end

function init(args)
  math.randomseed(os.time() + tonumber(string.sub(tostring({}), 8)) or 0)
end

------------------- генерация запросов -------------------
function request()
  if math.random() < 0.5 then
    -- /user/get/{uuid}
    local id = user_ids[math.random(#user_ids)]
    return wrk.format("GET", "/user/get/" .. id)
  else
    -- /user/search
    local first  = urlencode(rand_name())
    local second = urlencode(rand_name())
    return wrk.format("GET", "/user/search?first_name=" .. first .. "&second_name=" .. second)
  end
end

------------------- обработка ответов -------------------
function response(status, headers, body)
  if     status >= 200 and status < 300 then wrk.thread:set("c2", (wrk.thread:get("c2") or 0) + 1)
  elseif status >= 400 and status < 500 then wrk.thread:set("c4", (wrk.thread:get("c4") or 0) + 1)
  elseif status >= 500 and status < 600 then wrk.thread:set("c5", (wrk.thread:get("c5") or 0) + 1)
  else                                      wrk.thread:set("co", (wrk.thread:get("co") or 0) + 1)
  end
end

------------------- финальный отчёт -------------------
function done(summary, latency, requests)
  local s2, s4, s5, so = 0, 0, 0, 0
  for _, t in ipairs(threads) do
    s2 = s2 + (t:get("c2") or 0)
    s4 = s4 + (t:get("c4") or 0)
    s5 = s5 + (t:get("c5") or 0)
    so = so + (t:get("co") or 0)
  end
  io.stderr:write(string.format(
    "\nHTTP status counters:\n  2xx=%d\n  4xx=%d\n  5xx=%d\n  other=%d\n",
    s2, s4, s5, so
  ))
end
