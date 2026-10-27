# Creates the Uptime Kuma admin, its monitors and the Discord alert. Talks socket.io directly:
# uptime-kuma-api targets Kuma 1.x and its monitor schema drifts on 2.x. Idempotent by name.
# Settings come from the environment (the installer passes fxship.env).
import os, sys, time
import socketio

URL = "http://127.0.0.1:3001"
USER, PW = os.environ.get("KUMA_USER", "admin"), os.environ["KUMA_PASSWORD"]
name = os.environ.get("BRAND_NAME", "FiveM")
dev = os.environ.get("DEV_ENABLED", "true") == "true"

sio = socketio.Client()
# Kuma pushes these lists right after login: listen before logging in or they are missed.
monitors, notes = {}, []
sio.on("monitorList", lambda d: (monitors.clear(), monitors.update(d)))
sio.on("notificationList", lambda d: (notes.clear(), notes.extend(d)))
for attempt in range(30):  # Kuma refuses logins for a few seconds after it starts
    try:
        sio.connect(URL, wait_timeout=20)
        break
    except Exception:
        time.sleep(3)
else:
    sys.exit("can't reach Uptime Kuma on " + URL)

def call(ev, *args):
    for attempt in range(10):
        try:
            return sio.call(ev, data=args if len(args) != 1 else args[0], timeout=30)
        except socketio.exceptions.TimeoutError:
            time.sleep(5)
    sys.exit("Uptime Kuma didn't answer " + ev)

if call("needSetup"):
    print("setup:", call("setup", USER, PW))
for attempt in range(10):
    r = call("login", {"username": USER, "password": PW, "token": ""})
    if r.get("ok"):
        break
    time.sleep(3)
else:
    sys.exit("Kuma login failed: %s" % r)

for attempt in range(20):
    if monitors or attempt > 5:
        break
    time.sleep(1)

hook = os.environ.get("ALERT_WEBHOOK", "")
note_ids = {}
if hook:
    existing = next((n["id"] for n in notes if n["name"] == "Discord alerts"), None)
    # Not a default for every monitor: dev restarts all the time by design, so only prod's
    # monitors alert (see `alerts` below).
    body = {"name": "Discord alerts", "type": "discord", "discordWebhookUrl": hook, "discordUsername": name + " Status",
            "isDefault": False, "applyExisting": False, "active": True}
    r = call("addNotification", body, existing)
    print("discord alerts:", r.get("msg"))
    if r.get("id") or existing:
        note_ids = {str(r.get("id") or existing): True}

base = {"interval": 60, "retryInterval": 30, "maxretries": 2, "notificationIDList": note_ids, "accepted_statuscodes": ["200-299"],
        "active": True, "timeout": 24, "resendInterval": 0, "kafkaProducerBrokers": [], "kafkaProducerSaslOptions": {"mechanism": "None"},
        "conditions": [], "rabbitmqNodes": [], "method": "GET", "maxredirects": 10, "ignoreTls": False}
gw = "127.0.0.1"  # Kuma runs on the host network
# A game server is only "down" after about 3.5 minutes of failed checks, so a scheduled restart of a
# big server (several minutes to load hundreds of resources) doesn't page anyone. Alerts are sent
# once when a monitor goes down and once when it is back (resendInterval 0), never repeated.
alerts = {"notificationIDList": note_ids}
quiet = {"notificationIDList": {}}
game = {"interval": 60, "retryInterval": 30, "maxretries": 5}
want = [
    {"name": name + " prod", "type": "http", "url": "http://%s:%s/info.json" % (os.environ["PROD_IP"], os.environ.get("PROD_GAME_PORT", "30120")), **game, **alerts},
    {"name": "txAdmin prod", "type": "http", "url": "http://%s:40120/login" % gw, **alerts},
    {"name": "fxship agent", "type": "http", "url": "http://%s:7070/healthz" % gw, **alerts},
]
if dev:
    want += [
        {"name": name + " dev", "type": "http", "url": "http://%s:%s/info.json" % (os.environ["DEV_IP"], os.environ.get("DEV_GAME_PORT", "30130")), **game, **quiet},
        {"name": "txAdmin dev", "type": "http", "url": "http://%s:40130/login" % gw, **quiet},
    ]
# One monitor per name: older installers could add the same one twice. Keep the oldest.
have = {}
for m in sorted(monitors.values(), key=lambda m: m["id"]):
    if m["name"] in have:
        print("removing duplicate:", m["name"], call("deleteMonitor", m["id"]).get("msg"))
    else:
        have[m["name"]] = m
for m in want:
    old = have.get(m["name"])
    if old is None:
        print(m["name"], "->", call("add", {**base, **m}).get("msg"))
        continue
    # Re-runs bring existing monitors to the current settings, keeping anything else the owner set.
    cur = {k: old.get(k) for k in m}
    cur["notificationIDList"] = {str(k): True for k, v in (old.get("notificationIDList") or {}).items() if v}
    if cur == {**m, "notificationIDList": {str(k): True for k in m["notificationIDList"]}}:
        print("up to date:", m["name"])
        continue
    print(m["name"], "->", call("editMonitor", {**old, **m}).get("msg"))
sio.disconnect()
