import asyncio
import concurrent.futures
import os
import select
import socket
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from importlib import import_module

sys.path.insert(0, os.getcwd())
# ocel: imports
from ocel.worker import deliver

worker = os.environ.get("OCEL_WORKER")
if not worker:
    sys.exit("ocel: OCEL_WORKER names no worker, so this worker entry has nothing to serve")

loop = asyncio.new_event_loop()
threading.Thread(target=loop.run_forever, daemon=True).start()


class Delivery(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
        run = asyncio.run_coroutine_threadsafe(deliver(worker, body), loop)
        if not self.answered_before_hang_up(run):
            run.cancel()
            self.close_connection = True
            return
        status, answer = run.result()
        self.send_response(status)
        self.send_header("Content-Type", "application/json" if status in (200, 422) else "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(answer)))
        self.end_headers()
        self.wfile.write(answer)

    def answered_before_hang_up(self, run):
        while not run.done():
            readable, _, _ = select.select([self.connection], [], [], 0.25)
            if not readable:
                continue
            try:
                sent = self.connection.recv(1, socket.MSG_PEEK)
            except OSError:
                return False
            if not sent:
                return False
            concurrent.futures.wait([run])
        return True

    def log_message(self, format, *args):
        pass


ThreadingHTTPServer((os.environ.get("HOST") or "127.0.0.1", int(os.environ["PORT"])), Delivery).serve_forever()
