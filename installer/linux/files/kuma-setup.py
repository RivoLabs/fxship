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

monitors, notes = {}, []
sio.on("monitorList", lambda d: monitors.update(d))
sio.on("notificationList", lambda d: notes[:] or notes.extend(d))
time.sleep(3)

hook = os.environ.get("ALERT_WEBHOOK", "")
note_ids = {}
if hook:
    existing = next((n["id"] for n in notes if n["name"] == "Discord alerts"), None)
    body = {"name": "Discord alerts", "type": "discord", "discordWebhookUrl": hook, "discordUsername": name + " Status",
            "isDefault": True, "applyExisting": True, "active": True}
    r = call("addNotification", body, existing)
    print("discord alerts:", r.get("msg"))
    if r.get("id") or existing:
        note_ids = {str(r.get("id") or existing): True}

base = {"interval": 60, "retryInterval": 30, "maxretries": 2, "notificationIDList": note_ids, "accepted_statuscodes": ["200-299"],
        "active": True, "timeout": 24, "resendInterval": 0, "kafkaProducerBrokers": [], "kafkaProducerSaslOptions": {"mechanism": "None"},
        "conditions": [], "rabbitmqNodes": [], "method": "GET", "maxredirects": 10, "ignoreTls": False}
gw = "127.0.0.1"  # Kuma runs on the host network
want = [
    {"name": name + " prod", "type": "http", "url": "http://%s:%s/info.json" % (os.environ["PROD_IP"], os.environ.get("PROD_GAME_PORT", "30120"))},
    {"name": "txAdmin prod", "type": "http", "url": "http://%s:40120/login" % gw},
    {"name": "fxship agent", "type": "http", "url": "http://%s:7070/healthz" % gw},
]
if dev:
    want += [
        {"name": name + " dev", "type": "http", "url": "http://%s:%s/info.json" % (os.environ["DEV_IP"], os.environ.get("DEV_GAME_PORT", "30130"))},
        {"name": "txAdmin dev", "type": "http", "url": "http://%s:40130/login" % gw},
    ]
have = {m["name"] for m in monitors.values()}
for m in want:
    if m["name"] in have:
        print("exists:", m["name"])
        continue
    print(m["name"], "->", call("add", {**base, **m}).get("msg"))
sio.disconnect()
