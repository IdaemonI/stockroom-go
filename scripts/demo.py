"""Exercise the local Compose stack, or start a native API against DATABASE_URL."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
import subprocess
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen
from uuid import uuid4


def request(path, key="", headers=None, body=None):
    headers = dict(headers or {})
    if key:
        headers["Authorization"] = "Bearer " + key
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode()
    try:
        response = urlopen(Request("http://127.0.0.1:8080" + path, data, headers), timeout=5)
    except HTTPError as error:
        response = error
    with response:
        return response.status, json.load(response)


def ready():
    for _ in range(100):
        try:
            if request("/healthz")[0] == 200:
                return
        except (URLError, TimeoutError, ConnectionError):
            pass
        time.sleep(0.3)
    raise RuntimeError("API did not become ready")


def demo(restart):
    reader = os.getenv("STOCKROOM_READ_KEY", "demo-reader")
    writer = os.getenv("STOCKROOM_WRITE_KEY", "demo-writer")
    sku = "DEMO-" + uuid4().hex[:20].upper()

    def move(key, kind, quantity):
        return request("/v1/movements", writer, {"Idempotency-Key": sku + key},
                       {"sku": sku, "kind": kind, "quantity": quantity})

    ready()
    delivery = move("-dock", "receive", 2)
    assert delivery[0] == 201 and delivery[1]["balance"] == 2, delivery
    pick = move("-first", "issue", 1)
    assert pick[0] == 201 and pick[1]["balance"] == 1, pick
    restart()
    ready()
    replay = move("-dock", "receive", 2)
    assert replay == (200, delivery[1]), replay
    path = "/v1/stock/" + sku
    balance = request(path, reader)
    assert balance == (200, {"sku": sku, "balance": 1}), balance
    with ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(lambda key: move(key, "issue", 1), ["-A", "-B"]))
    statuses = sorted(status for status, _ in results)
    assert statuses == [201, 409], results
    assert request(path, reader) == (200, {"sku": sku, "balance": 0})
    status, history = request(path + "/movements", reader)
    assert status == 200 and len(history["movements"]) == 3, history
    print(f"{sku}: replay 200, receipt 2, live 1; picks {statuses}; final 0; ledger 3")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", help="launch this API binary with the exported database and keys")
    binary = parser.parse_args().binary
    if not binary:
        demo(lambda: subprocess.run(["docker", "compose", "restart", "api"], check=True))
        return
    for key in ("DATABASE_URL", "STOCKROOM_READ_KEY", "STOCKROOM_WRITE_KEY"):
        if not os.environ.get(key):
            parser.error(f"{key} is required for --binary")
    process = subprocess.Popen([binary])

    def stop():
        process.terminate()
        process.wait(timeout=10)

    def restart():
        nonlocal process
        stop()
        process = subprocess.Popen([binary])

    try:
        demo(restart)
    finally:
        stop()


if __name__ == "__main__":
    main()
